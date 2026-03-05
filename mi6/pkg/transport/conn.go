package transport

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	"golang.org/x/crypto/ssh"

	"james/mi6/pkg/auth"
	"james/mi6/pkg/protocol"
)

var (
	ErrUnauthorized = errors.New("transport: unauthorized public key")
	ErrAuthFailed   = errors.New("transport: authentication failed")
	ErrBadHandshake = errors.New("transport: handshake protocol error")
)

// SecureConn wraps a net.Conn with AES-256-GCM encryption after handshake.
type SecureConn struct {
	conn   net.Conn
	cipher cipher.AEAD
}

// ClientHandshake performs client-side auth: sends public key, receives challenge,
// signs it, sends back signature + ECDH pub key, receives auth confirmation.
// After success, all subsequent messages are encrypted.
func ClientHandshake(conn net.Conn, signer crypto.Signer, pubKey ssh.PublicKey) (*SecureConn, error) {
	// Step 1: Send MsgAuth with the SSH public key.
	authPayload := ssh.MarshalAuthorizedKey(pubKey)
	if err := protocol.WriteMessage(conn, &protocol.Message{
		Type:    protocol.MsgAuth,
		Payload: authPayload,
	}); err != nil {
		return nil, fmt.Errorf("transport: sending auth: %w", err)
	}

	// Step 2: Receive MsgAuthChallenge with challenge (32 bytes) + ECDH pub key (32 bytes).
	challengeMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("transport: reading challenge: %w", err)
	}
	if challengeMsg.Type != protocol.MsgAuthChallenge {
		if challengeMsg.Type == protocol.MsgAuthFail {
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("%w: expected MsgAuthChallenge, got %d", ErrBadHandshake, challengeMsg.Type)
	}
	if len(challengeMsg.Payload) != 64 {
		return nil, fmt.Errorf("%w: challenge payload must be 64 bytes", ErrBadHandshake)
	}

	challenge := challengeMsg.Payload[:32]
	serverECDHPubBytes := challengeMsg.Payload[32:64]

	// Step 3: Sign challenge, generate own ECDH keypair.
	sig, err := auth.SignChallenge(signer, challenge)
	if err != nil {
		return nil, fmt.Errorf("transport: signing challenge: %w", err)
	}

	clientECDH, err := auth.GenerateECDHKeyPair()
	if err != nil {
		return nil, fmt.Errorf("transport: generating ECDH keypair: %w", err)
	}

	// Step 4: Send MsgAuthResponse with [4-byte sig length][signature][ECDH pub key (32 bytes)].
	clientECDHPubBytes := clientECDH.PublicKey().Bytes()
	responsePayload := make([]byte, 4+len(sig)+len(clientECDHPubBytes))
	binary.BigEndian.PutUint32(responsePayload[:4], uint32(len(sig)))
	copy(responsePayload[4:4+len(sig)], sig)
	copy(responsePayload[4+len(sig):], clientECDHPubBytes)

	if err := protocol.WriteMessage(conn, &protocol.Message{
		Type:    protocol.MsgAuthResponse,
		Payload: responsePayload,
	}); err != nil {
		return nil, fmt.Errorf("transport: sending auth response: %w", err)
	}

	// Step 5: Receive MsgAuthOK or MsgAuthFail.
	resultMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("transport: reading auth result: %w", err)
	}
	if resultMsg.Type == protocol.MsgAuthFail {
		return nil, ErrAuthFailed
	}
	if resultMsg.Type != protocol.MsgAuthOK {
		return nil, fmt.Errorf("%w: expected MsgAuthOK, got %d", ErrBadHandshake, resultMsg.Type)
	}

	// Step 6: Derive session key from ECDH.
	serverECDHPub, err := ecdh.X25519().NewPublicKey(serverECDHPubBytes)
	if err != nil {
		return nil, fmt.Errorf("transport: parsing server ECDH public key: %w", err)
	}

	sessionKey, err := auth.DeriveSessionKey(clientECDH, serverECDHPub)
	if err != nil {
		return nil, fmt.Errorf("transport: deriving session key: %w", err)
	}

	aesCipher, err := newAESGCM(sessionKey)
	if err != nil {
		return nil, err
	}

	return &SecureConn{conn: conn, cipher: aesCipher}, nil
}

// ServerHandshake performs server-side auth: receives public key, checks authorized_keys,
// sends challenge + ECDH pub key, receives signature + client ECDH pub key, verifies, confirms.
func ServerHandshake(conn net.Conn, authorizedKeys []ssh.PublicKey) (*SecureConn, ssh.PublicKey, error) {
	// Step 1: Receive MsgAuth with the SSH public key.
	authMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: reading auth message: %w", err)
	}
	if authMsg.Type != protocol.MsgAuth {
		return nil, nil, fmt.Errorf("%w: expected MsgAuth, got %d", ErrBadHandshake, authMsg.Type)
	}

	// Parse the SSH public key from authorized_keys format.
	clientPubKey, _, _, _, err := ssh.ParseAuthorizedKey(authMsg.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: parsing client public key: %w", err)
	}

	// Step 2: Check if the key is authorized.
	if !auth.IsAuthorized(clientPubKey, authorizedKeys) {
		_ = protocol.WriteMessage(conn, &protocol.Message{
			Type:    protocol.MsgAuthFail,
			Payload: []byte("unauthorized"),
		})
		return nil, nil, ErrUnauthorized
	}

	// Step 3: Generate challenge + ECDH keypair, send MsgAuthChallenge.
	challenge, err := auth.GenerateChallenge()
	if err != nil {
		return nil, nil, fmt.Errorf("transport: generating challenge: %w", err)
	}

	serverECDH, err := auth.GenerateECDHKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("transport: generating ECDH keypair: %w", err)
	}

	challengePayload := make([]byte, 64)
	copy(challengePayload[:32], challenge)
	copy(challengePayload[32:], serverECDH.PublicKey().Bytes())

	if err := protocol.WriteMessage(conn, &protocol.Message{
		Type:    protocol.MsgAuthChallenge,
		Payload: challengePayload,
	}); err != nil {
		return nil, nil, fmt.Errorf("transport: sending challenge: %w", err)
	}

	// Step 4: Receive MsgAuthResponse with [4-byte sig length][signature][ECDH pub key].
	responseMsg, err := protocol.ReadMessage(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: reading auth response: %w", err)
	}
	if responseMsg.Type != protocol.MsgAuthResponse {
		return nil, nil, fmt.Errorf("%w: expected MsgAuthResponse, got %d", ErrBadHandshake, responseMsg.Type)
	}

	if len(responseMsg.Payload) < 4 {
		return nil, nil, fmt.Errorf("%w: auth response too short", ErrBadHandshake)
	}

	sigLen := binary.BigEndian.Uint32(responseMsg.Payload[:4])
	if uint32(len(responseMsg.Payload)) < 4+sigLen+32 {
		return nil, nil, fmt.Errorf("%w: auth response payload too short for sig+ECDH key", ErrBadHandshake)
	}

	signature := responseMsg.Payload[4 : 4+sigLen]
	clientECDHPubBytes := responseMsg.Payload[4+sigLen:]

	// Step 5: Verify signature.
	if err := auth.VerifyChallenge(clientPubKey, challenge, signature); err != nil {
		_ = protocol.WriteMessage(conn, &protocol.Message{
			Type:    protocol.MsgAuthFail,
			Payload: []byte("verification failed"),
		})
		return nil, nil, fmt.Errorf("transport: challenge verification failed: %w", err)
	}

	// Step 6: Derive session key.
	clientECDHPub, err := ecdh.X25519().NewPublicKey(clientECDHPubBytes)
	if err != nil {
		_ = protocol.WriteMessage(conn, &protocol.Message{
			Type:    protocol.MsgAuthFail,
			Payload: []byte("invalid ECDH key"),
		})
		return nil, nil, fmt.Errorf("transport: parsing client ECDH public key: %w", err)
	}

	sessionKey, err := auth.DeriveSessionKey(serverECDH, clientECDHPub)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: deriving session key: %w", err)
	}

	// Step 7: Send MsgAuthOK.
	if err := protocol.WriteMessage(conn, &protocol.Message{
		Type: protocol.MsgAuthOK,
	}); err != nil {
		return nil, nil, fmt.Errorf("transport: sending auth OK: %w", err)
	}

	aesCipher, err := newAESGCM(sessionKey)
	if err != nil {
		return nil, nil, err
	}

	return &SecureConn{conn: conn, cipher: aesCipher}, clientPubKey, nil
}

// Send encrypts and sends a protocol message.
// Frame format: [4-byte length][12-byte nonce][ciphertext+tag]
func (sc *SecureConn) Send(msg *protocol.Message) error {
	plaintext, err := protocol.Encode(msg)
	if err != nil {
		return fmt.Errorf("transport: encoding message: %w", err)
	}

	nonce := make([]byte, sc.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("transport: generating nonce: %w", err)
	}

	ciphertext := sc.cipher.Seal(nil, nonce, plaintext, nil)

	// Frame: [4-byte length of nonce+ciphertext][nonce][ciphertext]
	frameLen := len(nonce) + len(ciphertext)
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(frameLen))

	if _, err := sc.conn.Write(header); err != nil {
		return fmt.Errorf("transport: writing frame header: %w", err)
	}
	if _, err := sc.conn.Write(nonce); err != nil {
		return fmt.Errorf("transport: writing nonce: %w", err)
	}
	if _, err := sc.conn.Write(ciphertext); err != nil {
		return fmt.Errorf("transport: writing ciphertext: %w", err)
	}

	return nil
}

// Receive reads and decrypts a protocol message.
// Frame format: [4-byte length][12-byte nonce][ciphertext+tag]
func (sc *SecureConn) Receive() (*protocol.Message, error) {
	// Read 4-byte length header.
	var header [4]byte
	if _, err := io.ReadFull(sc.conn, header[:]); err != nil {
		return nil, fmt.Errorf("transport: reading frame header: %w", err)
	}

	frameLen := binary.BigEndian.Uint32(header[:])
	if frameLen > protocol.MaxMessageSize+uint32(sc.cipher.NonceSize())+uint32(sc.cipher.Overhead()) {
		return nil, fmt.Errorf("transport: encrypted frame too large: %d bytes", frameLen)
	}

	// Read nonce + ciphertext.
	frame := make([]byte, frameLen)
	if _, err := io.ReadFull(sc.conn, frame); err != nil {
		return nil, fmt.Errorf("transport: reading encrypted frame: %w", err)
	}

	nonceSize := sc.cipher.NonceSize()
	if int(frameLen) < nonceSize {
		return nil, fmt.Errorf("transport: frame too short for nonce")
	}

	nonce := frame[:nonceSize]
	ciphertext := frame[nonceSize:]

	plaintext, err := sc.cipher.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("transport: decrypting message: %w", err)
	}

	return protocol.Decode(plaintext)
}

// Close closes the underlying connection.
func (sc *SecureConn) Close() error {
	return sc.conn.Close()
}

// newAESGCM creates an AES-256-GCM cipher from a 32-byte key.
func newAESGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("transport: creating AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("transport: creating GCM: %w", err)
	}

	return gcm, nil
}
