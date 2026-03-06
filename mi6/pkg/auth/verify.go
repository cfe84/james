package auth

import (
	"crypto"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/ssh"
)

// GenerateChallenge creates a random 32-byte challenge.
func GenerateChallenge() ([]byte, error) {
	challenge := make([]byte, 32)
	_, err := rand.Read(challenge)
	if err != nil {
		return nil, err
	}
	return challenge, nil
}

// SignChallenge signs the challenge using the client's private key.
// Uses the ssh package's signing mechanism to produce an ssh.Signature,
// then marshals it with ssh.Marshal.
func SignChallenge(signer crypto.Signer, challenge []byte) ([]byte, error) {
	sshSigner, err := ssh.NewSignerFromSigner(signer)
	if err != nil {
		return nil, fmt.Errorf("creating ssh signer: %w", err)
	}

	sig, err := sshSigner.Sign(rand.Reader, challenge)
	if err != nil {
		return nil, fmt.Errorf("signing challenge: %w", err)
	}

	return ssh.Marshal(sig), nil
}

// VerifyChallenge verifies that the signature was produced by the holder of the given public key.
func VerifyChallenge(pubKey ssh.PublicKey, challenge []byte, signature []byte) error {
	var sig ssh.Signature
	if err := ssh.Unmarshal(signature, &sig); err != nil {
		return fmt.Errorf("unmarshaling signature: %w", err)
	}

	return pubKey.Verify(challenge, &sig)
}

// GenerateECDHKeyPair generates an ephemeral X25519 key pair for key exchange.
func GenerateECDHKeyPair() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// DeriveSessionKey performs ECDH with the peer's public key and derives a 32-byte
// AES-256 key using HKDF-SHA256.
// Salt is the sorted concatenation of both ECDH public keys, which binds the
// derived key to this specific exchange and prevents relay attacks.
func DeriveSessionKey(privKey *ecdh.PrivateKey, peerPubKey *ecdh.PublicKey) ([]byte, error) {
	shared, err := privKey.ECDH(peerPubKey)
	if err != nil {
		return nil, fmt.Errorf("ecdh exchange: %w", err)
	}

	// Build salt from both ECDH public keys (sorted for determinism).
	myPub := privKey.PublicKey().Bytes()
	peerPub := peerPubKey.Bytes()
	salt := make([]byte, len(myPub)+len(peerPub))
	// Deterministic order: smaller key first.
	if string(myPub) < string(peerPub) {
		copy(salt, myPub)
		copy(salt[len(myPub):], peerPub)
	} else {
		copy(salt, peerPub)
		copy(salt[len(peerPub):], myPub)
	}

	hkdfReader := hkdf.New(sha256.New, shared, salt, []byte("mi6-session-key"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("deriving key: %w", err)
	}

	return key, nil
}
