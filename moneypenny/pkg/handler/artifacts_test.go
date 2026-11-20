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
	"time"

	"james/moneypenny/pkg/agent"
	"james/moneypenny/pkg/artifact"
	"james/moneypenny/pkg/envelope"
)

func TestArtifactPublicationSnapshotAndSessionScope(t *testing.T) {
	h, params := gadgetTestHandler(t)
	setGadgetCaps(t, h, envelope.GadgetCapabilities{})
	for _, method := range []string{"artifacts.list", "artifacts.download"} {
		requireGadgetError(t, gadgetCall(t, params, method, map[string]string{"session_id": gadgetOtherSession}), "permission_denied")
	}
	list := func(sessionID string) []artifact.Metadata {
		t.Helper()
		data, _ := json.Marshal(envelope.SessionIDData{SessionID: sessionID})
		resp := h.Handle(context.Background(), &envelope.Command{Method: "list_artifacts", Data: data})
		items, ok := resp.Data.([]artifact.Metadata)
		if resp.Status != envelope.StatusSuccess || !ok {
			t.Fatalf("listing failed: %+v", resp)
		}
		return items
	}
	if items := list(gadgetSession); len(items) != 0 {
		t.Fatalf("empty session returned artifacts: %+v", items)
	}
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
	if items := list(gadgetSession); len(items) != 1 || items[0].ID != meta.ID ||
		items[0].Size != int64(len("next update")) || items[0].SentAt != turns[0].CreatedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("list did not return current artifact metadata: %+v", items)
	}
	for _, file := range []struct{ name, view string }{{"notes.MD", "markdown"}, {"output.txt", "text"}} {
		if err := os.WriteFile(filepath.Join(session.Path, file.name), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		resp := gadgetCall(t, params, "artifacts.publish", map[string]string{"path": file.name})
		raw, _ := json.Marshal(resp.Data)
		var doc artifact.Metadata
		if !resp.Success || json.Unmarshal(raw, &doc) != nil || doc.View != file.view {
			t.Fatalf("preview format missing from publication: %+v", resp)
		}
		// Simulate metadata written before text previews were supported.
		dir := filepath.Join(root, "artifacts", doc.ID)
		saved, err := loadStoredArtifact(dir)
		if err != nil {
			t.Fatal(err)
		}
		saved.View = ""
		raw, err = json.Marshal(saved)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "metadata.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		resp = gadgetCall(t, params, "artifacts.update", map[string]string{"id": doc.ID, "path": "report.html"})
		raw, _ = json.Marshal(resp.Data)
		var updated artifact.Metadata
		if !resp.Success || json.Unmarshal(raw, &updated) != nil || updated.Name != doc.Name || updated.View != doc.View {
			t.Fatalf("update did not preserve the legacy artifact's filename and preview: %+v", resp)
		}
	}
	deleteArtifact := func(session, id string) *envelope.Response {
		t.Helper()
		data, _ := json.Marshal(artifact.Ref{SessionID: session, ID: id})
		return h.Handle(context.Background(), &envelope.Command{Method: "delete_artifact", Data: data})
	}
	if resp := deleteArtifact(gadgetSession, "../"); resp.Status != envelope.StatusError {
		t.Fatal("invalid artifact ID was accepted")
	}
	if resp := deleteArtifact(gadgetOtherSession, meta.ID); resp.Status != envelope.StatusSuccess || len(list(gadgetSession)) != 3 {
		t.Fatal("delete crossed the session boundary")
	}
	requireGadgetError(t, gadgetCall(t, other, "artifacts.delete", artifact.DeleteRequest{ID: meta.ID}), "not_found")
	requireGadgetError(t, gadgetCall(t, params, "artifacts.delete", map[string]string{"id": meta.ID, "session_id": gadgetOtherSession}), "invalid_request")
	if resp := gadgetCall(t, params, "artifacts.delete", artifact.DeleteRequest{ID: meta.ID}); !resp.Success {
		t.Fatalf("publisher could not delete its own artifact: %+v", resp)
	}
	for range 2 {
		if resp := deleteArtifact(gadgetSession, meta.ID); resp.Status != envelope.StatusSuccess {
			t.Fatalf("artifact delete failed: %+v", resp)
		}

	}
	if resp := read(gadgetSession, meta.ID, 0); resp.Status != envelope.StatusError {
		t.Fatal("deleted snapshot remained readable")
	}
	turns, err = h.store.GetConversation(gadgetSession)
	if err != nil || len(turns) != 2 || len(list(gadgetSession)) != 2 {
		t.Fatalf("artifact delete did not remove just its own card and entry: %v %v", turns, err)
	}
	if _, err := os.Stat(filename); err != nil {
		t.Fatalf("artifact delete removed the original source: %v", err)
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

func TestProjectArtifactStagingIsNotPublished(t *testing.T) {
	h, _ := gadgetTestHandler(t)
	session, err := h.store.GetSession(gadgetSession)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session.Path, "replacement.txt"), []byte("# Notes"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(method string, data any) *envelope.Response {
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return h.Handle(context.Background(), &envelope.Command{Method: method, Data: raw})
	}
	resp := call("stage_artifact", artifact.StageRequest{SessionID: gadgetSession, Path: "replacement.txt", Name: "original.md"})
	meta, ok := resp.Data.(artifact.Metadata)
	if !ok || meta.Name != "original.md" || meta.View != "markdown" {
		t.Fatalf("staging did not use the published filename for preview: %+v", resp)
	}
	if turns, err := h.store.GetConversation(gadgetSession); err != nil || len(turns) != 0 {
		t.Fatalf("staging published a conversation turn: %+v %v", turns, err)
	}
	read := artifact.ReadRequest{SessionID: gadgetSession, ID: meta.ID}
	if resp := call("read_artifact", read); resp.Status != envelope.StatusError {
		t.Fatal("normal artifact route exposed staging")
	}
	if resp := call("read_staged_artifact", read); resp.Status != envelope.StatusSuccess || string(resp.Data.(artifact.Chunk).Content) != "# Notes" {
		t.Fatalf("staged read failed: %+v", resp)
	}
	if resp := call("delete_staged_artifact", artifact.Ref{SessionID: gadgetSession, ID: meta.ID}); resp.Status != envelope.StatusSuccess {
		t.Fatalf("stage cleanup failed: %+v", resp)
	}
	if resp := call("read_staged_artifact", read); resp.Status != envelope.StatusError {
		t.Fatal("deleted staged snapshot remained readable")
	}
}
