package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/channel"
	"james/moneypenny/pkg/store"
)

func TestChannelPluginDispatchesGatedMessageAndReply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test plugin launcher uses a POSIX shell")
	}
	t.Setenv("JAMES_CHANNEL_TEST_PLUGIN", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "teams-plugin")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestChannelPluginProcess$\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	h, s, sessionID := newOperationRepairHandler(t)
	h.channels = channel.NewPluginRegistry(path)
	defer h.channels.Close()
	if err := s.UpdateSessionStatus(sessionID, store.StateIdle); err != nil {
		t.Fatal(err)
	}
	ch := &store.Channel{SessionID: sessionID, Provider: "teams", TargetID: "test-chat",
		Enabled: true, Mention: "ian", LastSeenID: "baseline", LastSeenTS: "2026-11-10T12:00:00Z"}
	ch.ID, err = s.CreateChannel(ch)
	if err != nil {
		t.Fatal(err)
	}
	h.runAgentFunc = func(_ context.Context, params agent.RunParams) (*agent.Result, error) {
		if params.Prompt != "Owner: check" || params.ReplyChannelID != ch.ID {
			t.Errorf("gates or reply route lost: %+v", params)
		}
		return &agent.Result{Text: "Checked."}, nil
	}
	h.pollChannel(context.Background(), ch)
	h.runsWG.Wait()
	current, err := s.GetChannel(ch.ID)
	if err != nil || current.LastSeenID != "echo" || current.LastSeenTS != "2026-11-10T12:00:00.400000000Z" {
		t.Fatalf("fractional timestamp did not advance legacy cursor: %+v %v", current, err)
	}
	h.pollChannel(context.Background(), current)
	turns, err := s.GetConversation(sessionID)
	if err != nil || len(turns) != 2 || turns[0].Content != "Owner: check" || turns[1].Content != "Checked." {
		t.Fatalf("unexpected delivery or duplicate: %+v %v", turns, err)
	}
	h.drainOutbox(context.Background())
	items, err := s.PendingOutbox()
	if err != nil || len(items) != 0 {
		t.Fatalf("reply was not delivered: %+v %v", items, err)
	}
	sent, err := s.SentMessageIDs(ch.ID)
	if err != nil || !sent["sent-reply"] {
		t.Fatalf("reply echo marker not persisted: %+v %v", sent, err)
	}
}

func TestChannelPluginProcess(t *testing.T) {
	if os.Getenv("JAMES_CHANNEL_TEST_PLUGIN") != "1" {
		t.Skip("subprocess fixture")
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     string            `json:"id"`
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocol_version": "1"}
		case "self":
			result = map[string]string{"id": "owner", "name": "Owner"}
		case "list_messages":
			messages := []channel.Message{
				{ID: "other", Timestamp: "2026-11-10T12:00:00.100000000Z", SenderID: "other", Text: "@ian ignore"},
				{ID: "unaddressed", Timestamp: "2026-11-10T12:00:00.200000000Z", SenderID: "owner", Text: "ignore"},
				{ID: "human", Timestamp: "2026-11-10T12:00:00.300000000Z", SenderID: "owner", SenderName: "Owner", Text: "@ian check"},
				{ID: "echo", Timestamp: "2026-11-10T12:00:00.400000000Z", SenderID: "owner", Text: "[🤖 ian]\nIgnore own reply"},
			}
			after, err := time.Parse(time.RFC3339Nano, request.Params["since_ts"])
			if err != nil {
				os.Exit(2)
			}
			filtered := make([]channel.Message, 0)
			for _, message := range messages {
				at, _ := time.Parse(time.RFC3339Nano, message.Timestamp)
				if at.After(after) {
					filtered = append(filtered, message)
				}
			}
			result = filtered
		case "send":
			if request.Params["target_id"] != "test-chat" || request.Params["sender_name"] != "ian" ||
				request.Params["content"] != "Checked." {
				os.Exit(2)
			}
			result = map[string]string{"id": "sent-reply"}
		default:
			os.Exit(2)
		}
		if encoder.Encode(map[string]any{"type": "response", "id": request.ID, "result": result}) != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}
