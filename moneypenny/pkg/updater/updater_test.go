package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func releaseArchive(t *testing.T, goos, missing string) []byte {
	t.Helper()
	var buf bytes.Buffer
	names := []string{"moneypenny", "mi6-client", "hem", "gadgets"}
	if goos == "windows" {
		names = append(names, "moneypenny-update-helper")
	}
	names = append(names, "unrelated")
	if goos == "windows" {
		zw := zip.NewWriter(&buf)
		for _, name := range names {
			if name == missing {
				continue
			}
			w, err := zw.Create("james-windows-amd64/" + name + ".exe")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte("new " + name)); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, name := range names {
			if name == missing {
				continue
			}
			content := []byte("new " + name)
			if err := tw.WriteHeader(&tar.Header{
				Name: "james-" + goos + "-arm64/" + name,
				Mode: 0644, Size: int64(len(content)),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(content); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func TestExtractArchiveBinaries(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			dir := t.TempDir()
			u := New("1.77.0", "cfe84/james", dir, &mockChecker{idle: true})
			if err := u.extractArchive(bytes.NewReader(releaseArchive(t, goos, "")), dir, goos); err != nil {
				t.Fatal(err)
			}
			suffix := ""
			names := []string{"moneypenny", "mi6-client", "hem", "gadgets"}
			if goos == "windows" {
				suffix = ".exe"
				names = append(names, "moneypenny-update-helper")
			}
			for _, name := range names {
				path := filepath.Join(dir, name+suffix)
				content, err := os.ReadFile(path)
				if err != nil || string(content) != "new "+name {
					t.Fatalf("%s: content = %q, error = %v", name, content, err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
					t.Errorf("%s: permissions = %o, want 755", name, info.Mode().Perm())
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "unrelated"+suffix)); !os.IsNotExist(err) {
				t.Fatalf("unwanted binary was extracted: %v", err)
			}
		})
	}
}

func TestExtractArchiveMissingRequiredBinary(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, missing := range []string{"moneypenny", "hem", "gadgets", "moneypenny-update-helper"} {
			if missing == "moneypenny-update-helper" && goos != "windows" {
				continue
			}
			t.Run(goos+"/"+missing, func(t *testing.T) {
				dir := t.TempDir()
				u := New("1.77.0", "cfe84/james", dir, &mockChecker{idle: true})
				err := u.extractArchive(bytes.NewReader(releaseArchive(t, goos, missing)), dir, goos)
				want := missing + " binary not found in archive"
				if missing == "moneypenny-update-helper" {
					want = "update helper not found in archive"
				}
				if err == nil || err.Error() != want {
					t.Fatalf("error = %v, want %q", err, want)
				}
			})
		}
	}
}

func TestInstallGadgets(t *testing.T) {
	for _, suffix := range []string{"", ".exe"} {
		for _, existing := range []bool{false, true} {
			name := "new"
			if existing {
				name = "upgrade"
			}
			t.Run(name+suffix, func(t *testing.T) {
				staged, installed := t.TempDir(), t.TempDir()
				source := filepath.Join(staged, "gadgets"+suffix)
				target := filepath.Join(installed, "gadgets"+suffix)
				if err := os.WriteFile(source, []byte("new gadgets"), 0755); err != nil {
					t.Fatal(err)
				}
				if existing {
					if err := os.WriteFile(target, []byte("old gadgets"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				if err := installGadgets(staged, installed, suffix); err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(target)
				if err != nil || string(content) != "new gadgets" {
					t.Fatalf("content = %q, error = %v", content, err)
				}
				info, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0755 {
					t.Errorf("permissions = %o, want 755", info.Mode().Perm())
				}
				if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
					t.Fatalf("backup was not removed: %v", err)
				}
			})
		}
	}
}

func TestInstallGadgetsMissingStagePreservesInstalled(t *testing.T) {
	staged, installed := t.TempDir(), t.TempDir()
	target := filepath.Join(installed, "gadgets")
	if err := os.WriteFile(target, []byte("old gadgets"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := installGadgets(staged, installed, ""); err == nil || !strings.Contains(err.Error(), "staged gadgets binary not found") {
		t.Fatalf("error = %v, want missing staged gadgets error", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "old gadgets" {
		t.Fatalf("old binary not preserved: content = %q, error = %v", content, err)
	}
}

func TestInstallGadgetsInstallError(t *testing.T) {
	staged := t.TempDir()
	source := filepath.Join(staged, "gadgets")
	if err := os.WriteFile(source, []byte("new gadgets"), 0755); err != nil {
		t.Fatal(err)
	}
	err := installGadgets(staged, filepath.Join(t.TempDir(), "missing"), "")
	if err == nil || !strings.Contains(err.Error(), "swap gadgets:") {
		t.Fatalf("error = %v, want gadgets installation error", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("staged binary lost on installation failure: %v", err)
	}
}

func TestAtomicSwapFailedReplacementRestoresExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gadgets")
	if err := os.WriteFile(target, []byte("old gadgets"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicSwap(filepath.Join(dir, "missing"), target); err == nil {
		t.Fatal("expected missing replacement error")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "old gadgets" {
		t.Fatalf("old binary not restored: content = %q, error = %v", content, err)
	}
}

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
