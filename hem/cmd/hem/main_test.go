package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"james/hem/pkg/cli"
)

func TestGadgetFingerprintForwarding(t *testing.T) {
	if os.Getenv("HEM_TEST_GADGET_CLI") == "1" {
		os.Args = []string{"hem", "--hem", "relay.example:443/control",
			"--mi6-server-fingerprint", "SHA256:trusted", "list", "sessions"}
		main()
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX fake mi6-client")
	}
	dir := t.TempDir()
	client := filepath.Join(dir, "mi6-client")
	// Exit before any real connection, after recording the exact child arguments.
	if err := os.WriteFile(client, []byte("#!/bin/sh\nprintf 'ARG=%s\\n' \"$@\" >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEM_TEST_GADGET_CLI", "1")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MI6_SERVER_FINGERPRINT", "SHA256:environment-must-not-win")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGadgetFingerprintForwarding$")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if err == nil {
		t.Fatal("expected fake mi6-client to exit with an error")
	}
	if !strings.Contains(string(out), "ARG=--server-fingerprint\nARG=SHA256:trusted\nARG=relay.example:443/control\n") {
		t.Fatalf("Hem did not forward the explicit fingerprint to mi6-client:\n%s", out)
	}
}

func TestVersionDoesNotRequireRemoteFingerprint(t *testing.T) {
	if os.Getenv("HEM_TEST_LOCAL_VERSION") == "1" {
		os.Args = []string{"hem", "--hem", "relay.example:443/control", "version"}
		main()
		return
	}
	t.Setenv("HEM_TEST_LOCAL_VERSION", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestVersionDoesNotRequireRemoteFingerprint$")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if err != nil {
		t.Fatalf("hem version should not require a remote fingerprint: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), Version+"\n") {
		t.Fatalf("hem version output = %q, want line %q", strings.TrimSpace(string(out)), Version)
	}
}

func TestSetDefaultMI6DoesNotRequireServer(t *testing.T) {
	if os.Getenv("HEM_TEST_LOCAL_MI6_DEFAULT") == "1" {
		os.Args = []string{"hem", "set-default", "mi6", "relay.example/control"}
		main()
		return
	}
	t.Setenv("HEM_TEST_LOCAL_MI6_DEFAULT", "1")
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSetDefaultMI6DoesNotRequireServer$")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	if err != nil || !strings.Contains(string(out), `Default mi6 set to "relay.example/control".`) {
		t.Fatalf("offline set-default mi6 failed: %v\n%s", err, out)
	}
}

func TestIsStartServerCommand(t *testing.T) {
	if !isStartServerCommand([]string{"hem", "start", "server", "--mi6-control", "relay/control"}) {
		t.Fatal("start server command was not recognized")
	}
	for _, args := range [][]string{
		{"hem", "start"},
		{"hem", "start", "client"},
		{"hem", "list", "server"},
	} {
		if isStartServerCommand(args) {
			t.Fatalf("non-server command was recognized: %#v", args)
		}
	}
}

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

func TestAddGadgetProvenance(t *testing.T) {
	tests := []struct {
		name string
		cmd  *cli.Command
		want []string
	}{
		{"continue session", &cli.Command{Verb: "continue", Noun: "session", Args: []string{"target", "prompt"}}, []string{"target", "prompt", "--from", "source"}},
		{"create subsession", &cli.Command{Verb: "create", Noun: "subsession", Args: []string{"parent", "prompt"}}, []string{"parent", "prompt", "--from", "source"}},
		{"explicit source retained", &cli.Command{Verb: "create", Noun: "subsession", Args: []string{"parent", "--from", "explicit", "prompt"}}, []string{"parent", "--from", "explicit", "prompt"}},
		{"unrelated command", &cli.Command{Verb: "create", Noun: "session", Args: []string{"prompt"}}, []string{"prompt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addGadgetProvenance(tt.cmd, "source")
			if !reflect.DeepEqual(tt.cmd.Args, tt.want) {
				t.Fatalf("args = %#v, want %#v", tt.cmd.Args, tt.want)
			}
		})
	}
}
