package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
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
			if strings.Contains(string(payload), `"state":"probe_ready"`) {
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
