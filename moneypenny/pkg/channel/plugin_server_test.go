package channel

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

type testPluginProvider struct{}

func (testPluginProvider) Name() string { return "test" }
func (testPluginProvider) Caps() Caps   { return Caps{Search: true, ProvideID: true} }
func (testPluginProvider) Search(context.Context, string) ([]Target, error) {
	return []Target{{ID: "chat-1", Label: "Test chat"}}, nil
}

func TestPluginActivityWakesChannelManagerWithoutBlockingResponses(t *testing.T) {
	client, server := io.Pipe()
	reader, writer := io.Pipe()
	activity := make(chan struct{}, 1)
	c := newPluginClient("test")
	c.activity = activity
	done := make(chan struct{})
	go func() {
		c.serve(client, writer)
		close(done)
	}()
	defer reader.Close()
	defer func() {
		server.Close()
		<-done
	}()
	pending := make(chan pluginMessage, 1)
	c.pending["result"] = pending
	// Repeated signals coalesce: actual messages are read from persisted cursors.
	for i := 0; i < 50; i++ {
		if _, err := io.WriteString(server, `{"type":"event","event":"activity"}`+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := io.WriteString(server, `{"type":"response","id":"result","result":{"id":"sent"}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pending:
	case <-time.After(time.Second):
		t.Fatal("activity signals blocked the send response")
	}
	select {
	case <-activity:
	default:
		t.Fatal("plugin activity did not reach the channel manager")
	}
}
func (testPluginProvider) Resolve(context.Context, string) (Target, error) {
	return Target{ID: "chat-1", Label: "Test chat"}, nil
}
func (testPluginProvider) ListMessages(context.Context, string, string) ([]Message, error) {
	return []Message{{ID: "message-1", Text: "hello"}}, nil
}
func (testPluginProvider) Send(context.Context, string, string, string) (string, error) {
	return "sent-1", nil
}
func (testPluginProvider) LatestCursor(context.Context, string) (string, string, error) {
	return "message-1", "2026-09-04T00:00:00Z", nil
}
func (testPluginProvider) Self(context.Context) (string, string, error) {
	return "user-1", "Test user", nil
}
func (testPluginProvider) Close() {}

func TestRunPluginMethodDispatchesProviderOperations(t *testing.T) {
	ctx := context.Background()
	raw, err := json.Marshal(map[string]string{"target_id": "chat-1", "sender_name": "agent", "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runPluginMethod(ctx, testPluginProvider{}, "send", raw)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := result.(map[string]string)
	if !ok || got["id"] != "sent-1" {
		t.Fatalf("unexpected send result: %#v", result)
	}

	if _, err := runPluginMethod(ctx, testPluginProvider{}, "unknown", nil); err == nil {
		t.Fatal("expected unknown method error")
	}
}
