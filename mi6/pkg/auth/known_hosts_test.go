package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKnownHostFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	const address = "mi6.example:7007"
	const fingerprint = "SHA256:trusted-server-key"
	if err := os.WriteFile(path, []byte(address+" "+fingerprint+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := KnownHostFingerprint(path, address)
	if err != nil {
		t.Fatalf("KnownHostFingerprint: %v", err)
	}
	if got != fingerprint {
		t.Fatalf("KnownHostFingerprint = %q, want %q", got, fingerprint)
	}
}

func TestKnownHostFingerprintRejectsUnknownAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("mi6.example:7007 SHA256:trusted-server-key\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := KnownHostFingerprint(path, "other.example:7007"); err == nil {
		t.Fatal("KnownHostFingerprint succeeded for an untrusted address")
	}
}
