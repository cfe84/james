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
// Uses O_CREATE|O_EXCL to avoid TOCTOU races between concurrent server starts.
func LoadOrGenerateServerKey(keyPath string) (crypto.Signer, ssh.PublicKey, error) {
	// Try to load existing key first.
	signer, pub, err := LoadPrivateKey(keyPath)
	if err == nil {
		return signer, pub, nil
	}

	// Key doesn't exist or is unreadable — generate a new one.
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

	// Use O_CREATE|O_EXCL to atomically create the file — if another process
	// raced us, this fails and we load their key instead.
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			// Another process created the key — load it.
			return LoadPrivateKey(keyPath)
		}
		return nil, nil, fmt.Errorf("creating server key file: %w", err)
	}
	if _, err := f.Write(privPEM); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("writing server key: %w", err)
	}
	f.Close()

	pub, err = ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}

	// Also write public key for reference.
	pubAuth := ssh.MarshalAuthorizedKey(pub)
	_ = os.WriteFile(keyPath+".pub", pubAuth, 0644)

	return key, pub, nil
}
