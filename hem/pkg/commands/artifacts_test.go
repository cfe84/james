package commands

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

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
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			var command transport.Command
			if json.Unmarshal(scanner.Bytes(), &command) != nil {
				return
			}
			requests <- command
			var data any = artifact.Chunk{Artifact: artifact.Metadata{ID: "artifact"}, Content: []byte("chunk")}
			if command.Method == "list_artifacts" {
				data = []artifact.Metadata{{ID: "artifact"}}
			} else if command.Method == "delete_artifact" {
				data = command.Data
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
}
