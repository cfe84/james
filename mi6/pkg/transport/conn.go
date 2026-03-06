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
	"sync"
	"sync/atomic"

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
// Send is safe for concurrent use; Receive is not (callers must serialize reads).
type SecureConn struct {
	conn        net.Conn
	cipher      cipher.AEAD
	sendMu      sync.Mutex    // protects Send from concurrent writes
	sendCount   atomic.Uint64 // monotonic counter for nonce generation
	noncePrefix [4]byte       // random prefix set at init, differentiates connection restarts
}

// ClientHandshakeParams holds the parameters for a client handshake.
type ClientHandshakeParams struct {
	Conn           net.Conn
	Signer         crypto.Signer    // client SSH key for signing
	PubKey         ssh.PublicKey     // client SSH public key
	ServerAddr     string           // server address for TOFU lookup
	KnownHostsPath string          // path to known_hosts file (empty to skip TOFU)
}

// ClientHandshake performs the mutual-auth handshake:
// 1. Exchange ECDH public keys (plaintext, no identity leaked)
// 2. Derive session key, switch to encrypted channel
// 3. Server proves identity (SSH signature), client verifies (TOFU)
// 4. Client proves identity (SSH signature), server verifies (authorized_keys)
func ClientHandshake(params ClientHandshakeParams) (*SecureConn, error) {
	conn := params.Conn

	// Step 1: Generate ECDH keypair and send MsgHello with our ECDH pub.
	clientECDH, err := auth.GenerateECDHKeyPair()
	if err != nil {
		return nil, fmt.Errorf("transport: generating ECDH keypair: %w", err)
	}

	if err := protocol.WriteMessage(conn, &protocol.Message{
		Type:    protocol.MsgHello,
		Payload: clientECDH.PublicKey().Bytes(),
	}); err != nil {
		return nil, fmt.Errorf("transport: sending hello: %w", err)
	}

	// Step 2: Receive MsgServerHello with server's ECDH pub.
	serverHello, err := protocol.ReadMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("transport: reading server hello: %w", err)
	}
	if serverHello.Type != protocol.MsgServerHello {
		if serverHello.Type == protocol.MsgAuthFail {
			return nil, ErrAuthFailed
		}
		return nil, fmt.Errorf("%w: expected MsgServerHello, got %d", ErrBadHandshake, serverHello.Type)
	}
	if len(serverHello.Payload) != 32 {
		return nil, fmt.Errorf("%w: server ECDH pub must be 32 bytes", ErrBadHandshake)
	}

	serverECDHPub, err := ecdh.X25519().NewPublicKey(serverHello.Payload)
	if err != nil {
		return nil, fmt.Errorf("transport: parsing server ECDH public key: %w", err)
	}

	// Step 3: Derive session key. Everything encrypted from here.
	sessionKey, err := auth.DeriveSessionKey(clientECDH, serverECDHPub)
	if err != nil {
		return nil, fmt.Errorf("transport: deriving session key: %w", err)
	}

	aesCipher, err := newAESGCM(sessionKey)
	if err != nil {
		return nil, err
	}

	sc := &SecureConn{conn: conn, cipher: aesCipher}
	if _, err := rand.Read(sc.noncePrefix[:]); err != nil {
		return nil, fmt.Errorf("generating nonce prefix: %w", err)
	}

	// Step 4: Receive encrypted MsgServerAuth: [SSH pubkey (authorized_keys format)] + [4-byte sig len] + [signature].
	// The signature is over (client_ecdh_pub || server_ecdh_pub) — binds to this exchange.
	serverAuthMsg, err := sc.Receive()
	if err != nil {
		return nil, fmt.Errorf("transport: reading server auth: %w", err)
	}
	if serverAuthMsg.Type != protocol.MsgServerAuth {
		if serverAuthMsg.Type == protocol.MsgAuthFail {
			return nil, ErrAuthFailed
		}
		return nil, fmt.Errorf("%w: expected MsgServerAuth, got %d", ErrBadHandshake, serverAuthMsg.Type)
	}

	// Parse server SSH public key and signature.
	serverSSHPubKey, serverSig, err := parseAuthPayload(serverAuthMsg.Payload)
	if err != nil {
		return nil, fmt.Errorf("transport: parsing server auth: %w", err)
	}

	// Verify server's signature over the transcript (client_ecdh_pub || server_ecdh_pub).
	transcript := make([]byte, 64)
	copy(transcript[:32], clientECDH.PublicKey().Bytes())
	copy(transcript[32:], serverHello.Payload)
	if err := auth.VerifyChallenge(serverSSHPubKey, transcript, serverSig); err != nil {
		return nil, fmt.Errorf("transport: server identity verification failed: %w", err)
	}

	// TOFU: verify server fingerprint against known_hosts.
	if params.KnownHostsPath != "" && params.ServerAddr != "" {
		if err := auth.CheckKnownHost(params.KnownHostsPath, params.ServerAddr, serverSSHPubKey); err != nil {
			return nil, fmt.Errorf("transport: server identity: %w", err)
		}
	}

	// Step 5: Send encrypted MsgClientAuth: [SSH pubkey] + [signature of (server_ecdh_pub || client_ecdh_pub)].
	clientTranscript := make([]byte, 64)
	copy(clientTranscript[:32], serverHello.Payload)
	copy(clientTranscript[32:], clientECDH.PublicKey().Bytes())
	clientSig, err := auth.SignChallenge(params.Signer, clientTranscript)
	if err != nil {
		return nil, fmt.Errorf("transport: signing transcript: %w", err)
	}

	clientAuthPayload := buildAuthPayload(params.PubKey, clientSig)
	if err := sc.Send(&protocol.Message{
		Type:    protocol.MsgClientAuth,
		Payload: clientAuthPayload,
	}); err != nil {
		return nil, fmt.Errorf("transport: sending client auth: %w", err)
	}

	// Step 6: Receive MsgAuthOK or MsgAuthFail.
	resultMsg, err := sc.Receive()
	if err != nil {
		return nil, fmt.Errorf("transport: reading auth result: %w", err)
	}
	if resultMsg.Type == protocol.MsgAuthFail {
		return nil, ErrAuthFailed
	}
	if resultMsg.Type != protocol.MsgAuthOK {
		return nil, fmt.Errorf("%w: expected MsgAuthOK, got %d", ErrBadHandshake, resultMsg.Type)
	}

	return sc, nil
}

// ServerHandshakeParams holds the parameters for a server handshake.
type ServerHandshakeParams struct {
	Conn           net.Conn
	Signer         crypto.Signer    // server SSH key for signing
	PubKey         ssh.PublicKey     // server SSH public key
	AuthorizedKeys []ssh.PublicKey   // allowed client keys
}

// ServerHandshake performs the server side of the mutual-auth handshake.
func ServerHandshake(params ServerHandshakeParams) (*SecureConn, ssh.PublicKey, error) {
	conn := params.Conn

	// Step 1: Receive MsgHello with client's ECDH pub.
	clientHello, err := protocol.ReadMessage(conn)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: reading client hello: %w", err)
	}
	if clientHello.Type != protocol.MsgHello {
		return nil, nil, fmt.Errorf("%w: expected MsgHello, got %d", ErrBadHandshake, clientHello.Type)
	}
	if len(clientHello.Payload) != 32 {
		return nil, nil, fmt.Errorf("%w: client ECDH pub must be 32 bytes", ErrBadHandshake)
	}

	clientECDHPub, err := ecdh.X25519().NewPublicKey(clientHello.Payload)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: parsing client ECDH public key: %w", err)
	}

	// Step 2: Generate ECDH keypair, send MsgServerHello.
	serverECDH, err := auth.GenerateECDHKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("transport: generating ECDH keypair: %w", err)
	}

	if err := protocol.WriteMessage(conn, &protocol.Message{
		Type:    protocol.MsgServerHello,
		Payload: serverECDH.PublicKey().Bytes(),
	}); err != nil {
		return nil, nil, fmt.Errorf("transport: sending server hello: %w", err)
	}

	// Step 3: Derive session key. Everything encrypted from here.
	sessionKey, err := auth.DeriveSessionKey(serverECDH, clientECDHPub)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: deriving session key: %w", err)
	}

	aesCipher, err := newAESGCM(sessionKey)
	if err != nil {
		return nil, nil, err
	}

	sc := &SecureConn{conn: conn, cipher: aesCipher}
	if _, err := rand.Read(sc.noncePrefix[:]); err != nil {
		return nil, nil, fmt.Errorf("generating nonce prefix: %w", err)
	}

	// Step 4: Send encrypted MsgServerAuth: [SSH pubkey] + [signature of (client_ecdh_pub || server_ecdh_pub)].
	transcript := make([]byte, 64)
	copy(transcript[:32], clientHello.Payload)
	copy(transcript[32:], serverECDH.PublicKey().Bytes())
	serverSig, err := auth.SignChallenge(params.Signer, transcript)
	if err != nil {
		return nil, nil, fmt.Errorf("transport: signing transcript: %w", err)
	}

	serverAuthPayload := buildAuthPayload(params.PubKey, serverSig)
	if err := sc.Send(&protocol.Message{
		Type:    protocol.MsgServerAuth,
		Payload: serverAuthPayload,
	}); err != nil {
		return nil, nil, fmt.Errorf("transport: sending server auth: %w", err)
	}

	// Step 5: Receive encrypted MsgClientAuth: [SSH pubkey] + [signature].
	clientAuthMsg, err := sc.Receive()
	if err != nil {
		return nil, nil, fmt.Errorf("transport: reading client auth: %w", err)
	}
	if clientAuthMsg.Type != protocol.MsgClientAuth {
		return nil, nil, fmt.Errorf("%w: expected MsgClientAuth, got %d", ErrBadHandshake, clientAuthMsg.Type)
	}

	clientSSHPubKey, clientSig, err := parseAuthPayload(clientAuthMsg.Payload)
	if err != nil {
		_ = sc.Send(&protocol.Message{Type: protocol.MsgAuthFail, Payload: []byte("invalid auth payload")})
		return nil, nil, fmt.Errorf("transport: parsing client auth: %w", err)
	}

	// Check authorized_keys.
	if !auth.IsAuthorized(clientSSHPubKey, params.AuthorizedKeys) {
		_ = sc.Send(&protocol.Message{Type: protocol.MsgAuthFail, Payload: []byte("unauthorized")})
		return nil, nil, ErrUnauthorized
	}

	// Verify client's signature over (server_ecdh_pub || client_ecdh_pub).
	clientTranscript := make([]byte, 64)
	copy(clientTranscript[:32], serverECDH.PublicKey().Bytes())
	copy(clientTranscript[32:], clientHello.Payload)
	if err := auth.VerifyChallenge(clientSSHPubKey, clientTranscript, clientSig); err != nil {
		_ = sc.Send(&protocol.Message{Type: protocol.MsgAuthFail, Payload: []byte("verification failed")})
		return nil, nil, fmt.Errorf("transport: client signature verification failed: %w", err)
	}

	// Step 6: Send MsgAuthOK.
	if err := sc.Send(&protocol.Message{Type: protocol.MsgAuthOK}); err != nil {
		return nil, nil, fmt.Errorf("transport: sending auth OK: %w", err)
	}

	return sc, clientSSHPubKey, nil
}

// buildAuthPayload creates [SSH pubkey in authorized_keys format] + [4-byte sig len] + [signature].
func buildAuthPayload(pubKey ssh.PublicKey, sig []byte) []byte {
	keyBytes := ssh.MarshalAuthorizedKey(pubKey)
	payload := make([]byte, 4+len(keyBytes)+len(sig))
	binary.BigEndian.PutUint32(payload[:4], uint32(len(keyBytes)))
	copy(payload[4:4+len(keyBytes)], keyBytes)
	copy(payload[4+len(keyBytes):], sig)
	return payload
}

// parseAuthPayload extracts SSH pubkey and signature from an auth payload.
func parseAuthPayload(payload []byte) (ssh.PublicKey, []byte, error) {
	if len(payload) < 4 {
		return nil, nil, fmt.Errorf("auth payload too short")
	}
	keyLen := binary.BigEndian.Uint32(payload[:4])
	if uint32(len(payload)) < 4+keyLen {
		return nil, nil, fmt.Errorf("auth payload too short for key")
	}
	keyBytes := payload[4 : 4+keyLen]
	sig := payload[4+keyLen:]

	pubKey, _, _, _, err := ssh.ParseAuthorizedKey(keyBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing SSH public key: %w", err)
	}

	return pubKey, sig, nil
}

// Send encrypts and sends a protocol message.
// Frame format: [4-byte length][12-byte nonce][ciphertext+tag]
// Safe for concurrent use.
func (sc *SecureConn) Send(msg *protocol.Message) error {
	plaintext, err := protocol.Encode(msg)
	if err != nil {
		return fmt.Errorf("transport: encoding message: %w", err)
	}

	// Counter-based nonce: 4 random bytes (set at init) + 8-byte monotonic counter.
	// This prevents nonce reuse even for very long-lived connections.
	nonce := make([]byte, sc.cipher.NonceSize())
	count := sc.sendCount.Add(1)
	copy(nonce[:4], sc.noncePrefix[:])
	binary.BigEndian.PutUint64(nonce[4:], count)

	ciphertext := sc.cipher.Seal(nil, nonce, plaintext, nil)

	// Build the complete frame to write atomically.
	frameLen := len(nonce) + len(ciphertext)
	frame := make([]byte, 4+frameLen)
	binary.BigEndian.PutUint32(frame[:4], uint32(frameLen))
	copy(frame[4:], nonce)
	copy(frame[4+len(nonce):], ciphertext)

	sc.sendMu.Lock()
	_, err = sc.conn.Write(frame)
	sc.sendMu.Unlock()
	if err != nil {
		return fmt.Errorf("transport: writing frame: %w", err)
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
	maxFrame := uint32(protocol.MaxMessageSize) + uint32(sc.cipher.NonceSize()) + uint32(sc.cipher.Overhead())
	if frameLen > maxFrame {
		return nil, fmt.Errorf("transport: encrypted frame too large: %d bytes", frameLen)
	}

	// Read nonce + ciphertext.
	frame := make([]byte, int(frameLen))
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
