package channel

import (
	"context"
	"encoding/json"
	"testing"
)

type testPluginProvider struct{}

func (testPluginProvider) Name() string { return "test" }
func (testPluginProvider) Caps() Caps   { return Caps{Search: true, ProvideID: true} }
func (testPluginProvider) Search(context.Context, string) ([]Target, error) {
	return []Target{{ID: "chat-1", Label: "Test chat"}}, nil
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
