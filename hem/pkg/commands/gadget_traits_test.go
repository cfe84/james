package commands

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"james/hem/pkg/store"
	"james/hem/pkg/transport"
	"james/moneypenny/pkg/envelope"
)

func TestTraitGadgetPersistenceAndFreshPermissions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hem.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := New(st, "")
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
	var detail atomic.Value
	detail.Store(`{"gadget_capabilities":{"traits":true}}`)
	var status atomic.Value
	status.Store(envelope.StatusSuccess)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(in)
		for scanner.Scan() {
			var cmd transport.Command
			if json.Unmarshal(scanner.Bytes(), &cmd) != nil {
				return
			}
			if cmd.Method != "get_session" || cmd.Data.(map[string]any)["session_id"] != "source" {
				t.Errorf("unexpected daemon command: %#v", cmd)
			}
			if json.NewEncoder(out).Encode(transport.Response{Status: status.Load().(string), Data: json.RawMessage(detail.Load().(string))}) != nil {
				return
			}
		}
	}()
	defer func() { _, _ = in.WriteString("\n"); <-done }()
	if err := st.AddMoneypenny(&store.Moneypenny{Name: "mp", Enabled: true, TransportType: store.TransportFIFO, FIFOIn: inPath, FIFOOut: outPath}); err != nil {
		t.Fatal(err)
	}
	if err := st.TrackSession("source", "mp"); err != nil {
		t.Fatal(err)
	}
	for _, trait := range []*store.Trait{
		{ID: "trait-1", Name: "--default", Prompt: "original", EnabledByDefault: true},
		{ID: "trait-2", Name: "unrelated", Prompt: "unchanged"},
		{ID: "trait-3", Name: "unowned", Prompt: "must remain unchanged"},
	} {
		if err := st.CreateTrait(trait); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetSessionTraits("source", []string{"trait-1", "trait-2"}); err != nil {
		t.Fatal(err)
	}
	original, _ := st.GetTrait("trait-1")
	const body = " \n--default=false --name=forged\nUnicode 🕴\n"
	for _, tc := range []struct {
		method string
		data   any
	}{
		{"traits.list", map[string]any{}},
		{"traits.get", map[string]string{"id": "--default"}},
		{"traits.edit", map[string]string{"id": "trait-1", "body": body}},
	} {
		resp := gadgetRouteRequest(t, e, "source", tc.method, tc.data)
		if resp.Status != "ok" {
			t.Fatalf("%s: %s", tc.method, resp.Message)
		}
		if tc.method == "traits.list" {
			var table TableResult
			if json.Unmarshal(resp.Data, &table) != nil || len(table.Rows) != 3 {
				t.Fatalf("list did not reuse trait listing: %s", resp.Data)
			}
		} else {
			var result TraitResult
			if json.Unmarshal(resp.Data, &result) != nil || result.ID != "trait-1" || result.Name != original.Name || !result.EnabledByDefault {
				t.Fatalf("bad trait result: %s", resp.Data)
			}
			if tc.method == "traits.edit" && result.Prompt != body {
				t.Fatalf("replacement body changed: %q", result.Prompt)
			}
		}
		if resp := gadgetRouteRequest(t, e, "source", "traits.edit", map[string]string{"id": "trait-3", "body": "forged"}); resp.Status != "error" || !strings.Contains(resp.Message, "assigned to their own session") {
			t.Fatalf("unassigned trait edit was accepted: %+v", resp)
		}
	}
	// Read through another connection to verify persistence, not a result cache.
	reopened, err := store.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetTrait("trait-1")
	if err != nil || got.Prompt != body || got.Name != original.Name || got.EnabledByDefault != original.EnabledByDefault || !got.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("edit changed unrelated fields: %#v %v", got, err)
	}
	for _, tc := range []struct{ method, data string }{
		{"traits.edit", `{"id":"trait-1"}`},
		{"traits.edit", `{"id":"trait-1","body":null}`},
		{"traits.edit", `{"id":"trait-1","body":false}`},
		{"traits.edit", `{"body":"missing ID"}`},
		{"traits.get", `{"id":""}`},
		{"traits.get", `{"id":"absent"}`},
		{"traits.edit", `{"id":"absent","body":"no creation"}`},
		{"traits.edit", `{"id":"trait-1","body":"x","name":"forged"}`},
		{"traits.edit", `{"id":"trait-1","body":"x","enabled_by_default":false}`},
		{"traits.edit", `{"id":"trait-1","body":"x","source_session_id":"other"}`},
		{"traits.edit", `{"id":"trait-1","body":"x","gadget_capabilities":{"traits":true}}`},
		{"traits.get", `{"id":"trait-1","body":"not a get"}`},
		{"traits.list", `null`},
		{"traits.list", `[]`},
		{"traits.list", `{"method":"traits.edit"}`},
		{"traits.create", `{"name":"forged"}`},
		{"traits.delete", `{"id":"trait-1"}`},
		{"traits.assign", `{"id":"trait-1"}`},
	} {
		resp := gadgetRouteRequest(t, e, "source", tc.method, json.RawMessage(tc.data))
		if resp.Status != "error" {
			t.Fatalf("accepted %s %s: %+v", tc.method, tc.data, resp)
		}
	}
	for _, denied := range []string{`{}`, `{"gadget_capabilities":null}`, `{"gadget_capabilities":{"traits":false,"agents":true}}`, `{"gadget_capabilities":{"traits":"true"}}`} {
		detail.Store(denied)
		for _, method := range []string{"traits.list", "traits.get", "traits.edit"} {
			data := map[string]string{}
			if method != "traits.list" {
				data["id"] = "trait-1"
			}
			if method == "traits.edit" {
				data["body"] = "denied"
			}
			if resp := gadgetRouteRequest(t, e, "source", method, data); resp.Status != "error" {
				t.Fatalf("stale/missing grant accepted: %s %s", method, denied)
			}
		}
	}
	got, _ = reopened.GetTrait("trait-1")
	if got.Prompt != body {
		t.Fatal("rejected requests modified the trait")
	}
	other, _ := reopened.GetTrait("trait-2")
	unowned, _ := reopened.GetTrait("trait-3")
	ids, _ := reopened.GetSessionTraits("source")
	if other.Prompt != "unchanged" || unowned.Prompt != "must remain unchanged" || !reflect.DeepEqual(ids, []string{"trait-1", "trait-2"}) {
		t.Fatal("unrelated trait or assignments changed")
	}
	detail.Store(`{"gadget_capabilities":{"traits":true}}`)
	if resp := gadgetRouteRequest(t, e, "source", "traits.edit", map[string]string{"id": "--default", "body": ""}); resp.Status != "ok" {
		t.Fatalf("regrant/clear failed: %s", resp.Message)
	}
	if resp := gadgetRouteRequest(t, e, "missing", "traits.list", map[string]any{}); resp.Status != "error" || !strings.Contains(resp.Message, "not tracked") {
		t.Fatal("untracked source was accepted")
	}
	got, _ = reopened.GetTrait("trait-1")
	if got.Prompt != "" || got.Name != "--default" || !got.EnabledByDefault {
		t.Fatal("clear did not preserve unrelated fields")
	}
	for _, unavailable := range []string{"error", "unexpected"} {
		status.Store(unavailable)
		if resp := gadgetRouteRequest(t, e, "source", "traits.edit", map[string]string{"id": "trait-1", "body": "must not change"}); resp.Status != "error" {
			t.Fatalf("failed permission lookup allowed edit: %s", unavailable)
		}
	}
	got, _ = reopened.GetTrait("trait-1")
	if got.Prompt != "" {
		t.Fatal("failed lookup modified trait")
	}
}
