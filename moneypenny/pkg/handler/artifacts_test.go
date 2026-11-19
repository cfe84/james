package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/artifact"
	"james/moneypenny/pkg/envelope"
)

func TestArtifactPublicationSnapshotAndSessionScope(t *testing.T) {
	h, params := gadgetTestHandler(t)
	setGadgetCaps(t, h, envelope.GadgetCapabilities{})
	session, err := h.store.GetSession(gadgetSession)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(session.Path, "report.html")
	original := bytes.Repeat([]byte("report"), artifact.ChunkSize/3)
	if err := os.WriteFile(filename, original, 0600); err != nil {
		t.Fatal(err)
	}
	published := gadgetCall(t, params, "artifacts.publish", map[string]string{"path": "report.html", "title": "Review"})
	if !published.Success {
		t.Fatalf("publish without grants: %+v", published)
	}
	raw, _ := json.Marshal(published.Data)
	var meta artifact.Metadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.View != "html" || meta.Size != int64(len(original)) || meta.Title != "Review" {
		t.Fatalf("invalid metadata: %+v", meta)
	}
	turns, err := h.store.GetConversation(gadgetSession)
	if err != nil || len(turns) != 1 || turns[0].Role != "artifact" {
		t.Fatalf("publication not in conversation: %+v %v", turns, err)
	}
	var turnMeta artifact.Metadata
	if err := json.Unmarshal([]byte(turns[0].Content), &turnMeta); err != nil || turnMeta != meta {
		t.Fatalf("conversation metadata mismatch: %+v %v", turnMeta, err)
	}
	if err := os.WriteFile(filename, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	read := func(session, id string, offset int64) *envelope.Response {
		t.Helper()
		data, _ := json.Marshal(artifact.ReadRequest{SessionID: session, ID: id, Offset: offset})
		// Retrieval must not depend on the daemon instance that published it.
		reader := &Handler{store: h.store, dataDir: h.dataDir}
		return reader.readArtifact(context.Background(), &envelope.Command{Method: "read_artifact", Data: data})
	}
	var restored []byte
	for int64(len(restored)) < meta.Size {
		resp := read(gadgetSession, meta.ID, int64(len(restored)))
		var chunk artifact.Chunk
		raw, _ := json.Marshal(resp.Data)
		if resp.Status != envelope.StatusSuccess || json.Unmarshal(raw, &chunk) != nil || chunk.Artifact != meta || len(chunk.Content) > artifact.ChunkSize {
			t.Fatalf("invalid chunk: %+v", resp)
		}
		restored = append(restored, chunk.Content...)
	}
	sum := sha256.Sum256(restored)
	if !bytes.Equal(restored, original) || hex.EncodeToString(sum[:]) != meta.SHA256 {
		t.Fatal("published artifact was not an immutable snapshot")
	}
	for _, tc := range []struct {
		session, id string
		offset      int64
	}{
		{gadgetOtherSession, meta.ID, 0}, {gadgetSession, "../content", 0},
		{gadgetSession, meta.ID, -1}, {gadgetSession, meta.ID, meta.Size + 1},
	} {
		if resp := read(tc.session, tc.id, tc.offset); resp.Status != envelope.StatusError {
			t.Fatalf("invalid read accepted: %+v", tc)
		}
	}
	requireGadgetError(t, gadgetCall(t, params, "artifacts.publish", map[string]string{"path": filename, "session_id": gadgetOtherSession}), "invalid_request")
	for _, path := range []string{"missing.html", session.Path} {
		if resp := gadgetCall(t, params, "artifacts.publish", map[string]string{"path": path}); resp.Success {
			t.Fatalf("invalid source accepted: %s", path)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ = json.Marshal(map[string]string{"path": filename})
	if _, err := h.publishArtifact(cancelled, gadgetSession, raw); err == nil {
		t.Fatal("cancelled publication succeeded")
	}
	root, _ := h.sessionDirPath(gadgetSession)
	entries, err := os.ReadDir(filepath.Join(root, "artifacts"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed publication left partial files: %v %v", entries, err)
	}
	oversized := filepath.Join(session.Path, "oversized.bin")
	f, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(artifact.MaxFileSize + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if resp := gadgetCall(t, params, "artifacts.publish", map[string]string{"path": oversized}); resp.Success {
		t.Fatal("oversized publication accepted")
	}
	update := func(id, path string) {
		t.Helper()
		if resp := gadgetCall(t, params, "artifacts.update", map[string]string{"id": id, "path": path}); resp.Success {
			t.Fatalf("invalid update unexpectedly succeeded: %s %s", id, path)
		}
	}
	update("../content", filename)
	update(newAgentSessionID(), filename)
	update(meta.ID, oversized)
	other := agent.RunParams{}
	if err := h.prepareRunInstructions(gadgetOtherSession, &other); err != nil {
		t.Fatal(err)
	}
	if resp := gadgetCall(t, other, "artifacts.update", map[string]string{"id": meta.ID, "path": filename}); resp.Success {
		t.Fatal("another session replaced the artifact")
	}
	if resp := read(gadgetSession, meta.ID, 0); resp.Status != envelope.StatusSuccess || resp.Data.(artifact.Chunk).Artifact != meta {
		t.Fatalf("failed updates changed the original snapshot: %+v", resp)
	}
	for _, body := range []string{"changed", "next update"} {
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		resp := gadgetCall(t, params, "artifacts.update", map[string]string{"id": meta.ID, "path": "report.html"})
		if !resp.Success {
			t.Fatalf("update failed: %+v", resp)
		}
		result := read(gadgetSession, meta.ID, 0)
		chunk, ok := result.Data.(artifact.Chunk)
		if result.Status != envelope.StatusSuccess || !ok || string(chunk.Content) != body ||
			chunk.Artifact.Title != meta.Title || chunk.Artifact.Name != meta.Name || chunk.Artifact.View != meta.View {
			t.Fatalf("replacement was not served under the original identity: %+v", result)
		}
	}
	turns, err = h.store.GetConversation(gadgetSession)
	if err != nil || len(turns) != 1 || turns[0].Role != "artifact" {
		t.Fatalf("update added a conversation card: %+v %v", turns, err)
	}
	files, err := os.ReadDir(filepath.Join(root, "artifacts", meta.ID))
	if err != nil || len(files) != 2 {
		t.Fatalf("superseded snapshots were not cleaned: %v %v", files, err)
	}
	data, _ := json.Marshal(envelope.SessionIDData{SessionID: gadgetSession})
	if resp := h.Handle(context.Background(), &envelope.Command{Method: "delete_session", Data: data}); resp.Status != envelope.StatusSuccess {
		t.Fatalf("session deletion failed: %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(root, "artifacts")); !os.IsNotExist(err) {
		t.Fatal("session deletion retained artifacts")
	}
	if resp := read(gadgetSession, meta.ID, 0); resp.Status != envelope.StatusError {
		t.Fatal("deleted artifact remained accessible")
	}
}
