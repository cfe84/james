package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// LoadOrGenerateServerKey loads a server SSH key from keyPath, or generates one
// if it doesn't exist. Returns the signer and public key.
func LoadOrGenerateServerKey(keyPath string) (crypto.Signer, ssh.PublicKey, error) {
	if _, err := os.Stat(keyPath); err == nil {
		return LoadPrivateKey(keyPath)
	}

	// Generate a new ECDSA P-256 key.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating server key: %w", err)
	}

	privBytes, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling server key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, nil, err
	}

	privPEM := pem.EncodeToMemory(privBytes)
	if err := os.WriteFile(keyPath, privPEM, 0600); err != nil {
		return nil, nil, fmt.Errorf("saving server key: %w", err)
	}

	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}

	// Also write public key for reference.
	pubAuth := ssh.MarshalAuthorizedKey(pub)
	_ = os.WriteFile(keyPath+".pub", pubAuth, 0644)

	return key, pub, nil
}
