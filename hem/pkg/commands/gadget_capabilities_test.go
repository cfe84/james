package commands

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"james/hem/pkg/protocol"
	"james/hem/pkg/store"
	"james/hem/pkg/transport"
	"james/moneypenny/pkg/envelope"
	mpstore "james/moneypenny/pkg/store"
)

func TestGadgetCapabilityFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--gadget-memory=false", "prompt", "--gadget-agents=true", "--gadget-traits=true"},
		{"prompt", "--gadget-memory", "false", "--gadget-agents", "true", "--gadget-traits", "true"},
	} {
		var flags gadgetCapabilityFlags
		remaining, err := parseFlagsFromArgs("test", args, flags.register)
		if err != nil || !reflect.DeepEqual(remaining, []string{"prompt"}) {
			t.Fatalf("parse %v: remaining=%v err=%v", args, remaining, err)
		}
		want := envelope.GadgetCapabilities{Memory: false, Subagents: true, Agents: true, Traits: true, Scheduling: true}
		if got := flags.apply(nil); got == nil || *got != want {
			t.Fatalf("capabilities = %#v, want %#v", got, want)
		}
	}
	for _, value := range []string{"yes", "1", "", "FALSE"} {
		var flags gadgetCapabilityFlags
		if _, err := parseFlagsFromArgs("test", []string{"--gadget-agents=" + value}, flags.register); err == nil {
			t.Fatalf("accepted invalid permission %q", value)
		}
	}
	var flags gadgetCapabilityFlags
	if got := flags.apply(nil); got != nil {
		t.Fatalf("absent permission flags should remain nil, got %#v", got)
	}
	// Equals-form flags must not steal following positional arguments.
	rest, err := parseFlagsFromArgs("test", []string{"--name=value", "id", "prompt"}, func(fs *flag.FlagSet) {
		fs.String("name", "", "")
	})
	if err != nil || !reflect.DeepEqual(rest, []string{"id", "prompt"}) {
		t.Fatalf("equals-form parsing: %v, %v", rest, err)
	}
}

func TestGadgetCapabilityDefaultsAndExplicitFalse(t *testing.T) {
	for _, data := range []string{`{}`, `{"gadget_capabilities":null}`} {
		got, err := sessionGadgetCapabilities(json.RawMessage(data))
		if err != nil || got != envelope.DefaultGadgetCapabilities() {
			t.Fatalf("legacy detail %s: %#v, %v", data, got, err)
		}
	}
	allFalse := &envelope.GadgetCapabilities{}
	cmd, err := buildCreateSessionData(&sessionParams{GadgetCapabilities: allFalse}, "new", "hello")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var decoded envelope.CreateSessionData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GadgetCapabilities == nil || *decoded.GadgetCapabilities != *allFalse {
		t.Fatalf("lost explicit false permissions: %s", raw)
	}
	cmd, err = buildCreateSessionData(&sessionParams{}, "new", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := cmd["gadget_capabilities"]; present {
		t.Fatal("absent create permissions should be left to the daemon defaults")
	}
}

func TestGadgetCapabilityStorePersistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "moneypenny.db")
	s, err := mpstore.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	const initial = `{"memory":false,"subagents":true,"agents":true,"traits":true,"scheduling":false}`
	if err := s.CreateSession(&mpstore.Session{SessionID: "session", GadgetCapabilities: initial}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession("session")
	if err != nil || got.GadgetCapabilities != initial {
		t.Fatalf("create/get lost permissions: %#v, %v", got, err)
	}
	// Existing callers that omit the new argument must preserve permissions.
	name := "renamed"
	if err := s.UpdateSessionFields("session", &name, nil, nil, nil, nil, nil, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListSessions()
	if err != nil || len(all) != 1 || all[0].GadgetCapabilities != initial {
		t.Fatalf("list/update lost permissions: %#v, %v", all, err)
	}
	replacement := `{"memory":false,"subagents":false,"agents":false,"scheduling":false}`
	if err := s.UpdateSessionFields("session", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, &replacement); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = mpstore.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSession("session")
	if err != nil || got.GadgetCapabilities != replacement || got.Name != name {
		t.Fatalf("reopen lost settings: %#v, %v", got, err)
	}
	if err := s.CreateSession(&mpstore.Session{SessionID: "legacy"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSession("legacy")
	if err != nil || got.GadgetCapabilities != "" {
		t.Fatalf("absent settings should retain default sentinel: %#v, %v", got, err)
	}
}

func TestGadgetCapabilityStoreMigration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	s, err := mpstore.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(&mpstore.Session{SessionID: "legacy", Name: "unchanged"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE sessions DROP COLUMN gadget_capabilities"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = mpstore.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetSession("legacy")
	if err != nil || got == nil || got.Name != "unchanged" || got.GadgetCapabilities != "" {
		t.Fatalf("legacy session migration failed: %#v, %v", got, err)
	}
}

func TestGadgetCapabilitiesCommandForwarding(t *testing.T) {
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
	writes := make(chan json.RawMessage, 16)
	original := envelope.GadgetCapabilities{Memory: false, Subagents: true, Agents: true, Traits: true, Scheduling: false}
	defer func() { _, _ = in.WriteString("\n") }()
	go func() {
		source := map[string]interface{}{
			"session_id": "source", "name": "source", "agent": "copilot",
			"path": ".", "gadget_capabilities": original,
		}
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			var command struct {
				Method string                     `json:"method"`
				Data   map[string]json.RawMessage `json:"data"`
			}
			if json.Unmarshal(scanner.Bytes(), &command) != nil {
				return
			}
			data := interface{}(map[string]interface{}{})
			switch command.Method {
			case "get_session":
				data = source
			case "update_session":
				if caps, ok := command.Data["gadget_capabilities"]; ok {
					source["gadget_capabilities"] = caps
				}
				writes <- command.Data["gadget_capabilities"]
			case "create_session":
				writes <- command.Data["gadget_capabilities"]
			case "summarize_session":
				data = map[string]interface{}{"summary": "source summary", "turn_count": 1}
			}
			raw, _ := json.Marshal(data)
			if err := json.NewEncoder(out).Encode(transport.Response{Status: "ok", Data: raw}); err != nil {
				return
			}
		}
	}()
	if err := e.store.AddMoneypenny(&store.Moneypenny{Name: "mp", Enabled: true, TransportType: store.TransportFIFO, FIFOIn: inPath, FIFOOut: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.TrackSession("source", "mp"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetCachedModels("mp", "copilot", []store.CachedModelEntry{{Name: "model", Value: "model"}}); err != nil {
		t.Fatal(err)
	}
	check := func(resp *protocol.Response, want envelope.GadgetCapabilities) {
		t.Helper()
		if resp.Status != "ok" {
			t.Fatalf("command failed: %s", resp.Message)
		}
		select {
		case data := <-writes:
			var got envelope.GadgetCapabilities
			if err := json.Unmarshal(data, &got); err != nil || got != want {
				t.Fatalf("forwarded permissions %s, want %#v (err=%v)", data, want, err)
			}
		case <-time.After(time.Second):
			t.Fatal("permissions were not forwarded")
		}
	}
	check(e.CreateSession([]string{"-m", "mp", "--agent", "copilot", "--async", "--gadget-memory=false", "--gadget-traits=true", "hello"}),
		envelope.GadgetCapabilities{Subagents: true, Traits: true, Scheduling: true})
	check(e.CreateSubSession([]string{"source", "--async", "--gadget-scheduling=false", "hello"}),
		envelope.GadgetCapabilities{Memory: true, Subagents: true})
	updated := original
	updated.Subagents = false
	check(e.UpdateSession([]string{"source", "--gadget-subagents=false"}), updated)
	shown := e.ShowSession([]string{"source"})
	if shown.Status != "ok" {
		t.Fatalf("show failed: %s", shown.Message)
	}
	var detail SessionShowResult
	if err := json.Unmarshal(shown.Data, &detail); err != nil || detail.GadgetCapabilities != updated {
		t.Fatalf("show lost permissions: %s, %v", shown.Data, err)
	}
	check(e.CopySession([]string{"source", "--async"}), updated)
	copied := updated
	copied.Agents = false
	check(e.CopySession([]string{"source", "--async", "--gadget-agents=false"}), copied)
	copied = updated
	copied.Traits = false
	check(e.CopySession([]string{"source", "--async", "--gadget-traits=false"}), copied)
	updated.Traits = false
	check(e.UpdateSession([]string{"source", "--gadget-traits=false"}), updated)
	if resp := e.UpdateSession([]string{"source", "--gadget-agents=maybe"}); resp.Status != "error" || !strings.Contains(resp.Message, "true or false") {
		t.Fatalf("invalid boolean was accepted: %#v", resp)
	}
}
