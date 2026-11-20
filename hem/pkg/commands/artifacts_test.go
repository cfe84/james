package commands

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/hem/pkg/transport"
	"james/moneypenny/pkg/artifact"
	"james/moneypenny/pkg/envelope"
)

func TestArtifactCommandsRouteOnlyTrackedSessions(t *testing.T) {
	e := newHierarchyExecutor(t)
	dir := t.TempDir()
	inPath, outPath := filepath.Join(dir, "in"), filepath.Join(dir, "out")
	for _, path := range []string{inPath, outPath} {
		if err := syscall.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	in, err := os.OpenFile(inPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(outPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	requests := make(chan transport.Command, 2)
	done := make(chan struct{})
	body := bytes.Repeat([]byte("report"), artifact.StageChunkSize/6+1)
	hash := sha256.Sum256(body)
	snapshot := artifact.Metadata{ID: generateSessionID(), Name: "report.md", Title: "Report", View: "markdown", Size: int64(len(body)), SHA256: hex.EncodeToString(hash[:])}
	stages := make(chan artifact.StageRequest, 2)
	cleaned := make(chan struct{}, 3)
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			var command transport.Command
			if json.Unmarshal(scanner.Bytes(), &command) != nil {
				return
			}
			rawRequest, _ := json.Marshal(command.Data)
			var data any = artifact.Chunk{Artifact: artifact.Metadata{ID: "artifact"}, Content: []byte("chunk")}
			switch command.Method {
			case "stage_artifact":
				var request artifact.StageRequest
				_ = json.Unmarshal(rawRequest, &request)
				stages <- request
				data = snapshot
			case "read_staged_artifact":
				var request artifact.ReadRequest
				_ = json.Unmarshal(rawRequest, &request)
				end := min(request.Offset+artifact.StageChunkSize, int64(len(body)))
				data = artifact.Chunk{Artifact: snapshot, Content: body[request.Offset:end]}
			case "delete_staged_artifact":
				cleaned <- struct{}{}
				data = command.Data
			case "list_artifacts":
				requests <- command
				data = []artifact.Metadata{{ID: "artifact"}}
			case "delete_artifact":
				requests <- command
				data = command.Data
			default:
				requests <- command
			}
			raw, _ := json.Marshal(data)
			if err := json.NewEncoder(out).Encode(transport.Response{Status: "success", Data: raw}); err != nil {
				return
			}
		}
	}()
	defer func() { _, _ = in.WriteString("\n"); <-done }()
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mp", Enabled: true, TransportType: store.TransportFIFO, FIFOIn: inPath, FIFOOut: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.TrackSession("session", "mp"); err != nil {
		t.Fatal(err)
	}
	request := artifact.ReadRequest{SessionID: "session", ID: "artifact", Offset: 192}
	raw, _ := json.Marshal(request)
	resp := e.Dispatch("read", "artifact", []string{string(raw)})
	var chunk artifact.Chunk
	if resp.Status != "ok" || json.Unmarshal(resp.Data, &chunk) != nil || string(chunk.Content) != "chunk" {
		t.Fatalf("read failed: %+v", resp)
	}
	cmd := <-requests
	var forwarded artifact.ReadRequest
	data, _ := json.Marshal(cmd.Data)
	if cmd.Method != "read_artifact" || json.Unmarshal(data, &forwarded) != nil || !reflect.DeepEqual(forwarded, request) {
		t.Fatalf("read lost target or offset: %+v", cmd)
	}
	resp = e.Dispatch("list", "artifact", []string{"session"})
	var items []artifact.Metadata
	if resp.Status != "ok" || json.Unmarshal(resp.Data, &items) != nil || len(items) != 1 || items[0].ID != "artifact" {
		t.Fatalf("list failed: %+v", resp)
	}
	cmd = <-requests
	var list envelope.SessionIDData
	data, _ = json.Marshal(cmd.Data)
	if cmd.Method != "list_artifacts" || json.Unmarshal(data, &list) != nil || list.SessionID != "session" {
		t.Fatalf("list lost the tracked session: %+v", cmd)
	}
	wantDelete := artifact.Ref{SessionID: "session", ID: "artifact"}
	if resp := e.Dispatch("delete", "artifact", []string{wantDelete.SessionID, wantDelete.ID}); resp.Status != "ok" {
		t.Fatalf("delete failed: %+v", resp)
	}
	cmd = <-requests
	var deleted artifact.Ref
	data, _ = json.Marshal(cmd.Data)
	if cmd.Method != "delete_artifact" || json.Unmarshal(data, &deleted) != nil || deleted != wantDelete {
		t.Fatalf("delete lost the tracked session or artifact: %+v", cmd)
	}
	if resp := e.Dispatch("delete", "artifact", []string{"missing", "artifact"}); resp.Status != "error" {
		t.Fatal("delete accepted unknown session")
	}
	for _, args := range [][]string{nil, {"missing"}, {"session", "other"}} {
		if resp := e.Dispatch("list", "artifact", args); resp.Status != "error" {
			t.Fatalf("invalid artifact list accepted: %v", args)
		}
	}
	for _, payload := range []string{`{"session_id":"missing","id":"artifact"}`, `{"session_id":"session","id":"artifact","path":"/private"}`} {
		if resp := e.Dispatch("read", "artifact", []string{payload}); resp.Status != "error" {
			t.Fatalf("invalid artifact read accepted: %s", payload)
		}
	}
	select {
	case cmd := <-requests:
		t.Fatalf("invalid request reached a daemon: %+v", cmd)
	default:
	}

	for _, id := range []string{"project", "other"} {
		if err := e.store.CreateProject(&store.Project{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.store.SetSessionProject("session", "project"); err != nil {
		t.Fatal(err)
	}
	for session, project := range map[string]string{"peer": "project", "outsider": "other", "unassigned": ""} {
		if err := e.store.TrackSession(session, "mp", project); err != nil {
			t.Fatal(err)
		}
	}
	call := func(session, method string, data any, status string) *protocol.Response {
		t.Helper()
		body, err := json.Marshal(map[string]any{"source_session_id": session, "method": "artifacts." + method, "data": data})
		if err != nil {
			t.Fatal(err)
		}
		resp := e.Dispatch("gadget", "route", []string{string(body)})
		if resp.Status != status {
			t.Fatalf("%s artifacts.%s: %+v", session, method, resp)
		}
		return resp
	}
	publish := artifact.PublishRequest{Path: "source.md", Project: true}
	call("unassigned", "publish", publish, "error")
	resp = call("session", "publish", publish, "ok")
	var meta artifact.Metadata
	if err := json.Unmarshal(resp.Data, &meta); err != nil || meta.ProjectID != "project" || meta.ID == snapshot.ID || meta.SentAt == "" {
		t.Fatalf("unexpected project snapshot metadata: %s (%v)", resp.Data, err)
	}
	if stage := <-stages; stage.SessionID != "session" || stage.Path != "source.md" {
		t.Fatalf("publish read from the wrong source: %+v", stage)
	}
	<-cleaned
	resp = call("peer", "list", artifact.ListRequest{}, "ok")
	<-requests
	if err := json.Unmarshal(resp.Data, &items); err != nil || len(items) != 2 || items[0].ID != "artifact" || items[1] != meta {
		t.Fatalf("session list did not append its project artifacts: %s (%v)", resp.Data, err)
	}
	read := artifact.ReadRequest{ID: meta.ID, Project: true, Offset: artifact.ChunkSize}
	del := artifact.DeleteRequest{ID: meta.ID, Project: true}
	update := artifact.UpdateRequest{ID: meta.ID, Path: "replacement.txt", Project: true}
	call("outsider", "download", read, "error")
	call("outsider", "update", update, "error")
	call("outsider", "delete", del, "error")
	if err := e.store.DeleteTrackedSession("session"); err != nil {
		t.Fatal(err)
	}
	resp = call("peer", "download", read, "ok")
	if err := json.Unmarshal(resp.Data, &chunk); err != nil || chunk.Artifact != meta || !bytes.Equal(chunk.Content, body[artifact.ChunkSize:2*artifact.ChunkSize]) {
		t.Fatalf("project artifact unavailable after publisher deletion: %v", err)
	}
	call("peer", "update", update, "ok")
	if stage := <-stages; stage.SessionID != "peer" || stage.Name != meta.Name {
		t.Fatalf("peer update did not use its own host and preserve the original filename: %+v", stage)
	}
	<-cleaned
	if err := e.store.SetSessionProject("peer", "other"); err != nil {
		t.Fatal(err)
	}
	call("peer", "download", read, "error")
	if err := e.store.SetSessionProject("peer", "project"); err != nil {
		t.Fatal(err)
	}
	call("peer", "delete", del, "ok")
	call("peer", "download", read, "error")
	call("peer", "publish", publish, "ok")
	<-stages
	<-cleaned
	if err := e.store.DeleteProject("project"); err != nil {
		t.Fatal(err)
	}
	if items, err := e.store.ListProjectArtifacts("project", ""); err != nil || len(items) != 0 {
		t.Fatalf("project deletion retained snapshots: %+v %v", items, err)
	}
}
