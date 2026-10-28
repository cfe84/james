package commands

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"james/hem/pkg/store"
	"james/hem/pkg/transport"
	"james/moneypenny/pkg/envelope"
)

func TestPonsonbyRegistryAssignmentsAndRoutedConsultation(t *testing.T) {
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
	requests := make(chan transport.Command, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			var cmd transport.Command
			if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
				return
			}
			requests <- cmd
			data, _ := cmd.Data.(map[string]any)
			var response any = map[string]any{}
			switch cmd.Method {
			case "ponsonby_save", "ponsonby_get":
				response = data["store"]
			case "ponsonby_submit":
				response = data["job"]
			case "get_session":
				response = map[string]any{"environment": map[string]string{}}
			}
			raw, _ := json.Marshal(response)
			if json.NewEncoder(out).Encode(transport.Response{Status: envelope.StatusSuccess, Data: raw}) != nil {
				return
			}
		}
	}()
	defer func() { _, _ = in.WriteString("\n"); <-done }()
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mp", Enabled: true, IsDefault: true, TransportType: store.TransportFIFO, FIFOIn: inPath, FIFOOut: outPath}); err != nil {
		t.Fatal(err)
	}
	r := e.Dispatch("create", "ponsonby", []string{"--name=engineering", "--purpose=Engineering decisions", "--model=cheap-model", "--default"})
	if r.Status != "ok" {
		t.Fatal(r.Message)
	}
	stores, err := e.store.ListPonsonbies()
	if err != nil || len(stores) != 1 {
		t.Fatal(stores, err)
	}
	p := stores[0]
	if r := e.Dispatch("update", "ponsonby", []string{p.Name, "--introduction=ignored"}); r.Status != "error" {
		t.Fatal("unsupported update flag silently ignored")
	}
	if _, err := e.resolvePonsonbies("missing", false); err == nil {
		t.Fatal("unknown assignment accepted")
	}
	if _, err := e.resolvePonsonbies("engineering,engineering:read", false); err == nil {
		t.Fatal("duplicate conflicting modes accepted")
	}
	create := e.CreateSession([]string{"--async", "--agent=copilot", "Default memories"})
	if create.Status != "ok" {
		t.Fatal(create.Message)
	}
	var id string
	for id == "" {
		cmd := <-requests
		if cmd.Method == "create_session" {
			data := cmd.Data.(map[string]any)
			id = data["session_id"].(string)
			raw, _ := json.Marshal(data["ponsonbies"])
			var attachments []envelope.Ponsonby
			if json.Unmarshal(raw, &attachments) != nil || len(attachments) != 1 || !attachments[0].Publish || attachments[0].ID != p.ID {
				t.Fatal("defaults not sent to daemon", string(raw))
			}
		}
	}
	attachments, err := e.store.SessionPonsonbies(id)
	if err != nil || len(attachments) != 1 {
		t.Fatal("default assignments not persisted", attachments, err)
	}
	ask := gadgetRouteRequest(t, e, id, "ponsonby.ask", map[string]any{"store": "engineering", "body": "What decisions?"})
	if ask.Status != "ok" {
		t.Fatal(ask.Message)
	}
	for {
		cmd := <-requests
		if cmd.Method == "ponsonby_submit" {
			data := cmd.Data.(map[string]any)["job"].(map[string]any)
			if data["source_id"] != id || data["kind"] != "ask" {
				t.Fatal("consultation lost source binding", data)
			}
			break
		}
	}
	if r := e.UpdateSession([]string{id, "--ponsonbies=engineering:read"}); r.Status != "ok" {
		t.Fatal(r.Message)
	}
	read, err := e.store.SessionPonsonbies(id)
	if err != nil || len(read) != 1 || read[0].Publish {
		t.Fatal("read-only assignment not saved", read, err)
	}
	if r := gadgetRouteRequest(t, e, id, "ponsonby.ingest", map[string]any{"body": "private", "id": "turn-1"}); r.Status != "ok" {
		t.Fatal("read-only publication should skip delivery")
	}
	if r := gadgetRouteRequest(t, e, id, "ponsonby.ask", map[string]any{"store": p.ID, "body": "question", "source_id": "forged"}); r.Status != "error" {
		t.Fatal("accepted source override")
	}
	if r := e.Dispatch("delete", "ponsonby", []string{"engineering"}); r.Status != "error" {
		t.Fatal("deleted assigned store")
	}
	if r := e.UpdateSession([]string{id, "--ponsonbies="}); r.Status != "ok" {
		t.Fatal(r.Message)
	}
	if r := gadgetRouteRequest(t, e, id, "ponsonby.ask", map[string]any{"store": p.ID, "body": "question"}); r.Status != "error" || !strings.Contains(r.Message, "not assigned") {
		t.Fatal("removed assignment retained access", r.Message)
	}
	if r := e.Dispatch("delete", "ponsonby", []string{"engineering"}); r.Status != "ok" {
		t.Fatal(r.Message)
	}
	if stores, err := e.store.ListPonsonbies(); err != nil || len(stores) != 0 {
		t.Fatal("deleted store retained in registry", stores, err)
	}
	for {
		if cmd := <-requests; cmd.Method == "ponsonby_delete" {
			break
		}
	}
}
