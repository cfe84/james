package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestSignVerifyRSA(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	pubKey, err := ssh.NewPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}

	sig, err := SignChallenge(rsaKey, challenge)
	if err != nil {
		t.Fatal(err)
	}

	if err := VerifyChallenge(pubKey, challenge, sig); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestSignVerifyECDSA(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	pubKey, err := ssh.NewPublicKey(&ecKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}

	sig, err := SignChallenge(ecKey, challenge)
	if err != nil {
		t.Fatal(err)
	}

	if err := VerifyChallenge(pubKey, challenge, sig); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestVerifyFailsWithWrongKey(t *testing.T) {
	// Sign with one key
	rsaKey1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatal(err)
	}

	sig, err := SignChallenge(rsaKey1, challenge)
	if err != nil {
		t.Fatal(err)
	}

	// Verify with a different key
	rsaKey2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	wrongPubKey, err := ssh.NewPublicKey(&rsaKey2.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	if err := VerifyChallenge(wrongPubKey, challenge, sig); err == nil {
		t.Fatal("expected verification to fail with wrong key")
	}
}

func TestECDHKeyExchange(t *testing.T) {
	alicePriv, err := GenerateECDHKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	bobPriv, err := GenerateECDHKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	aliceKey, err := DeriveSessionKey(alicePriv, bobPriv.PublicKey())
	if err != nil {
		t.Fatal(err)
	}

	bobKey, err := DeriveSessionKey(bobPriv, alicePriv.PublicKey())
	if err != nil {
		t.Fatal(err)
	}

	if len(aliceKey) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(aliceKey))
	}

	for i := range aliceKey {
		if aliceKey[i] != bobKey[i] {
			t.Fatal("derived keys do not match")
		}
	}
}

func TestLoadAuthorizedKeysAndIsAuthorized(t *testing.T) {
	// Generate two test keys
	rsaKey1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubKey1, err := ssh.NewPublicKey(&rsaKey1.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	rsaKey2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubKey2, err := ssh.NewPublicKey(&rsaKey2.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	// A third key not in the authorized file
	rsaKey3, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubKey3, err := ssh.NewPublicKey(&rsaKey3.PublicKey)
	if err != nil {
		t.Fatal(err)
	}

	// Write authorized_keys file
	tmpDir := t.TempDir()
	authKeysPath := filepath.Join(tmpDir, "authorized_keys")

	line1 := string(ssh.MarshalAuthorizedKey(pubKey1))
	line2 := string(ssh.MarshalAuthorizedKey(pubKey2))
	content := line1 + line2

	if err := os.WriteFile(authKeysPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	// Load and check
	keys, err := LoadAuthorizedKeys(authKeysPath)
	if err != nil {
		t.Fatal(err)
	}

	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	if !IsAuthorized(pubKey1, keys) {
		t.Fatal("pubKey1 should be authorized")
	}
	if !IsAuthorized(pubKey2, keys) {
		t.Fatal("pubKey2 should be authorized")
	}
	if IsAuthorized(pubKey3, keys) {
		t.Fatal("pubKey3 should NOT be authorized")
	}
}
