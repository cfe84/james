package auth

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// LoadAuthorizedKeys reads an OpenSSH authorized_keys file and returns the parsed public keys.
func LoadAuthorizedKeys(path string) ([]ssh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var keys []ssh.PublicKey
	rest := data
	for len(rest) > 0 {
		// Skip blank lines
		line := strings.TrimSpace(string(rest))
		if line == "" {
			break
		}

		var key ssh.PublicKey
		key, _, _, rest, err = ssh.ParseAuthorizedKey(rest)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// LoadPrivateKey reads an OpenSSH private key file (RSA or ECDSA, PEM format).
// Returns the crypto.Signer for signing and the ssh.PublicKey for identification.
func LoadPrivateKey(path string) (crypto.Signer, ssh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return ParsePrivateKeyBytes(data)
}

// ParsePrivateKeyBytes parses a PEM-encoded private key from bytes.
// Returns the crypto.Signer for signing and the ssh.PublicKey for identification.
func ParsePrivateKeyBytes(data []byte) (crypto.Signer, ssh.PublicKey, error) {
	rawKey, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, nil, err
	}

	signer, ok := rawKey.(crypto.Signer)
	if !ok {
		return nil, nil, err
	}

	pubKey, err := ssh.NewPublicKey(signer.Public())
	if err != nil {
		return nil, nil, err
	}

	return signer, pubKey, nil
}

// GenerateKey generates a new ECDSA P-256 key pair and returns the private key
// in PEM format and the public key in OpenSSH authorized_keys format.
func GenerateKey() (privateKeyPEM []byte, publicKeyAuthorized []byte, error error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	privBytes, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, nil, err
	}

	privPEM := pem.EncodeToMemory(privBytes)

	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, err
	}

	pubAuth := ssh.MarshalAuthorizedKey(pub)

	return privPEM, pubAuth, nil
}

// IsAuthorized checks if a given public key is in the authorized keys list.
func IsAuthorized(key ssh.PublicKey, authorized []ssh.PublicKey) bool {
	keyBytes := key.Marshal()
	for _, ak := range authorized {
		if bytes.Equal(keyBytes, ak.Marshal()) {
			return true
		}
	}
	return false
}
