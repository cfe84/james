package updater

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"0.10.3", "0.10.2", true},
		{"0.10.2", "0.10.2", false},
		{"0.10.1", "0.10.2", false},
		{"0.11.0", "0.10.9", true},
		{"1.0.0", "0.99.99", true},
		{"0.10.2", "0.10.3", false},
	}
	for _, tt := range tests {
		got := isNewer(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  [3]int
	}{
		{"0.10.2", [3]int{0, 10, 2}},
		{"v1.2.3", [3]int{1, 2, 3}},
		{"0.0.1", [3]int{0, 0, 1}},
	}
	for _, tt := range tests {
		got := parseVersion(tt.input)
		if got != tt.want {
			t.Errorf("parseVersion(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

type mockChecker struct {
	idle bool
}

func (m *mockChecker) AllSessionsIdle() bool { return m.idle }

func TestStatusInitial(t *testing.T) {
	u := New("0.10.2", "cfe84/james", "/tmp/test", &mockChecker{idle: true})
	info := u.Status()
	if info.CurrentVersion != "0.10.2" {
		t.Errorf("CurrentVersion = %q, want %q", info.CurrentVersion, "0.10.2")
	}
	if info.Status != StatusUpToDate {
		t.Errorf("Status = %q, want %q", info.Status, StatusUpToDate)
	}
	if info.UpdateAvailable {
		t.Errorf("UpdateAvailable should be false initially")
	}
}

func TestWithBeforeRestart(t *testing.T) {
	called := false
	u := New("0.10.2", "cfe84/james", "/tmp/test", &mockChecker{idle: true},
		WithBeforeRestart(func() { called = true }),
	)
	if u.beforeRestart == nil {
		t.Fatal("beforeRestart was not configured")
	}
	u.beforeRestart()
	if !called {
		t.Fatal("beforeRestart callback was not invoked")
	}
}

func TestVerifyReleaseManifest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := json.Marshal(releaseManifest{
		Version: "1.65.1",
		Artifacts: map[string]string{
			"james-linux-amd64.tar.gz": strings.Repeat("a", 64),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifestBytes)
	publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKey)

	got, err := verifyReleaseManifestWithKey(manifestBytes, signature, "v1.65.1", publicKeyBase64)
	if err != nil {
		t.Fatalf("verifyReleaseManifestWithKey() error = %v", err)
	}
	if got.Version != "1.65.1" || got.Artifacts["james-linux-amd64.tar.gz"] != strings.Repeat("a", 64) {
		t.Fatalf("unexpected manifest: %+v", got)
	}
}

func TestVerifyReleaseManifestRejectsInvalidInput(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKey)
	manifestBytes := []byte(`{"version":"1.65.1","artifacts":{"james-linux-amd64.tar.gz":"` + strings.Repeat("a", 64) + `"}}`)
	signature := ed25519.Sign(privateKey, manifestBytes)

	tests := []struct {
		name      string
		manifest  []byte
		signature []byte
		tag       string
	}{
		{"bad signature", manifestBytes, []byte("bad"), "v1.65.1"},
		{"tag mismatch", manifestBytes, signature, "v1.65.2"},
		{"malformed manifest", []byte("{"), ed25519.Sign(privateKey, []byte("{")), "v1.65.1"},
		{"missing artifacts", []byte(`{"version":"1.65.1","artifacts":{}}`), ed25519.Sign(privateKey, []byte(`{"version":"1.65.1","artifacts":{}}`)), "v1.65.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := verifyReleaseManifestWithKey(tt.manifest, tt.signature, tt.tag, publicKeyBase64); err == nil {
				t.Fatal("expected verification error")
			}
		})
	}
}
