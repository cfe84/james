package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateReleaseKeypair(t *testing.T) {
	dir := t.TempDir()
	if err := generateReleaseKeypair([]string{"--output-dir", dir}); err != nil {
		t.Fatalf("generateReleaseKeypair() error = %v", err)
	}

	publicData, err := os.ReadFile(filepath.Join(dir, "james-release-public.key"))
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := os.ReadFile(filepath.Join(dir, "james-release-private.key"))
	if err != nil {
		t.Fatal(err)
	}
	privateBase64, err := os.ReadFile(filepath.Join(dir, "james-release-private.key.b64"))
	if err != nil {
		t.Fatal(err)
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		t.Fatalf("private key length = %d, want %d", len(privateKey), ed25519.PrivateKeySize)
	}
	if strings.TrimSpace(string(privateBase64)) != base64.StdEncoding.EncodeToString(privateKey) {
		t.Fatal("base64 private key does not match raw private key")
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(publicData)))
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.PublicKey(publicKey).Equal(ed25519.PrivateKey(privateKey).Public()) {
		t.Fatal("public key does not match private key")
	}
	if err := generateReleaseKeypair([]string{"--output-dir", dir}); err == nil {
		t.Fatal("expected existing files to prevent keypair regeneration")
	}
}
