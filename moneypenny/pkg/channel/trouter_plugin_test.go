package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunTrouterPluginDryRunLifecycle(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"request","id":"1","method":"initialize","version":"1"}`,
		`{"type":"request","id":"2","method":"configure","version":"1","params":{"dry_run":true}}`,
		`{"type":"request","id":"3","method":"authenticate","version":"1"}`,
		`{"type":"request","id":"4","method":"connect","version":"1"}`,
		`{"type":"request","id":"5","method":"shutdown","version":"1"}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := runTrouterPlugin(context.Background(), strings.NewReader(input), &output, false, ""); err != nil {
		t.Fatal(err)
	}

	var messages []pluginMessage
	dec := json.NewDecoder(&output)
	for {
		var msg pluginMessage
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}
	if len(messages) < 6 {
		t.Fatalf("got %d protocol messages, want lifecycle events and responses", len(messages))
	}
	foundReady := false
	foundStopped := false
	for _, msg := range messages {
		if msg.Type == "event" && msg.Event == "status" {
			payload, _ := json.Marshal(msg.Payload)
			if strings.Contains(string(payload), `"state":"dry_run_ready"`) {
				foundReady = true
			}
			if strings.Contains(string(payload), `"state":"stopped"`) {
				foundStopped = true
			}
		}
	}
	if !foundReady || !foundStopped {
		t.Fatalf("missing lifecycle events: ready=%v stopped=%v output=%s", foundReady, foundStopped, output.String())
	}
}

func TestTrouterRequiresAuthenticationBeforeConnect(t *testing.T) {
	p := newTrouterPlugin(false, "")
	if _, err := p.handle(context.Background(), "connect", nil); err == nil {
		t.Fatal("connect succeeded before authentication")
	}
}

func TestNativePollingUsesIC3TokenAndRenewsBeforeExpiry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Azure CLI fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "az")
	script := `#!/bin/sh
case "$*" in
  "account get-access-token --resource https://ic3.teams.office.com --output json")
    printf '%s\n' '{"accessToken":"test.eyJvaWQiOiJvd25lciJ9.signature"}' ;;
  "account show --output json")
    printf '%s\n' '{"user":{"name":"Owner"}}' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	p := newTrouterPlugin(false, "")
	defer p.close()
	ctx := context.Background()
	if err := p.authenticate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := p.handle(ctx, "connect", nil); err != nil {
		t.Fatal(err)
	}
	if status := p.status(); status["transport"] != "polling" || status["connection"] != "connected" {
		t.Fatalf("unexpected status: %+v", status)
	}
	previous := p.provider
	previous.expiresAt = time.Now().Add(-time.Second)
	if _, err := p.handle(ctx, "self", nil); err != nil || p.provider == previous || p.provider.selfID != "8:orgid:owner" {
		t.Fatalf("expired provider was not renewed: %v", err)
	}
	p.provider.expiresAt = time.Now().Add(-time.Second)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := p.handle(ctx, "self", nil); err == nil {
		t.Fatal("failed renewal was accepted")
	}
	if status := p.status(); status["auth"] != "not_started" || p.provider != nil || p.token != "" {
		t.Fatalf("failed renewal retained credentials: %+v", status)
	}
}

func TestTrouterDryRunNeverSendsAndReconfigurationClearsIdentity(t *testing.T) {
	p := newTrouterPlugin(true, "")
	if err := p.authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"send", "list_messages"} {
		if _, err := p.handle(context.Background(), method, nil); err == nil {
			t.Fatalf("dry run accepted %s", method)
		}
	}
	p.initialize(trouterConfig{AuthCommand: "/nonexistent/teams-auth"})
	if err := p.authenticate(context.Background()); err == nil {
		t.Fatal("authentication unexpectedly succeeded")
	}
	if status := p.status(); status["auth"] != "not_started" || status["account"] != "" || p.token != "" {
		t.Fatalf("stale authentication survived: %+v", status)
	}
}
