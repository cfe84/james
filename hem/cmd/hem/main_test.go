package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"james/hem/pkg/cli"
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
