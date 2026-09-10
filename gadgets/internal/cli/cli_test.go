package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestParseCommands(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		stdin  string
		method string
		data   string
	}{
		{"get root", []string{"memory", "get"}, "", "memory.get", `{"path":""}`},
		{"list", []string{"memory", "list", "project"}, "", "memory.list", `{"path":"project"}`},
		{"revisions", []string{"memory", "revisions"}, "", "memory.revisions", `{"path":""}`},
		{"search", []string{"memory", "search", "two words"}, "", "memory.search", `{"query":"two words"}`},
		{"set stdin", []string{"memory", "set", "project"}, "a\nb\n", "memory.set", `{"path":"project","body":"a\nb\n"}`},
		{"set empty", []string{"memory", "set", "--body="}, "ignored", "memory.set", `{"path":"","body":""}`},
		{"set flags first", []string{"memory", "set", "--body", "hello", "project"}, "", "memory.set", `{"path":"project","body":"hello"}`},
		{"set help body", []string{"memory", "set", "--body", "--help"}, "", "memory.set", `{"path":"","body":"--help"}`},
		{"batch", []string{"memory", "batch"}, `[{"path":"a","body":"A"},{"path":"b","body":"B"}]`, "memory.batch", `{"entries":[{"path":"a","body":"A"},{"path":"b","body":"B"}]}`},
		{"delete", []string{"memory", "delete", "project"}, "", "memory.delete", `{"path":"project","recursive":false}`},
		{"recursive", []string{"memory", "delete", "project", "--recursive"}, "", "memory.delete", `{"path":"project","recursive":true}`},
		{"nonrecursive", []string{"memory", "delete", "--recursive=false", "project"}, "", "memory.delete", `{"path":"project","recursive":false}`},
		{"agents list", []string{"agents", "list"}, "", "agents.list", `{}`},
		{"agents create", []string{"agents", "create", "--name", "--yolo", "--agent", "copilot", "--model", "m", "--path", "src", "--moneypenny", "remote"}, "--from=forged\n", "agents.create", `{"name":"--yolo","agent":"copilot","model":"m","path":"src","moneypenny":"remote","prompt":"--from=forged\n"}`},
		{"agents defaults", []string{"agents", "create"}, "prompt", "agents.create", `{"prompt":"prompt"}`},
		{"agents yolo", []string{"agents", "create", "--yolo"}, "prompt", "agents.create", `{"yolo":true,"prompt":"prompt"}`},
		{"traits list", []string{"traits", "list"}, "", "traits.list", `{}`},
		{"traits get", []string{"traits", "get", "clean code"}, "", "traits.get", `{"id":"clean code"}`},
		{"traits edit", []string{"traits", "edit", "id"}, " \n--name=forged\n🕴\n", "traits.edit", `{"id":"id","body":" \n--name=forged\n🕴\n"}`},
		{"traits clear", []string{"traits", "edit", "id"}, "", "traits.edit", `{"id":"id","body":""}`},
		{"traits literal ID", []string{"traits", "get", "--", "--default"}, "", "traits.get", `{"id":"--default"}`},
		{"sessions edit own", []string{"sessions", "edit", "--name", "new name", "--yolo=false"}, "", "sessions.edit", `{"name":"new name","yolo":false}`},
		{"sessions edit target", []string{"sessions", "edit", "target", "--model", "m"}, "", "sessions.edit", `{"session_id":"target","model":"m"}`},
		{"sessions edit environment", []string{"sessions", "edit", "--env", "KEY=value"}, "", "sessions.edit", `{"environment":{"KEY":"value"}}`},
		{"agents message", []string{"agents", "message", "id", "--body", "hello"}, "", "agents.message", `{"id":"id","body":"hello"}`},
		{"subagents list", []string{"subagents", "list"}, "", "subagents.list", `{}`},
		{"subagents message", []string{"subagents", "message", "id"}, "hello\n", "subagents.message", `{"id":"id","body":"hello\n"}`},
		{"subagents create", []string{"subagents", "create", "--name", "n", "--agent", "copilot", "--model", "m", "--path", "src"}, "prompt\n", "subagents.create", `{"name":"n","agent":"copilot","model":"m","path":"src","prompt":"prompt\n"}`},
		{"subagents defaults", []string{"subagents", "create"}, "prompt", "subagents.create", `{"prompt":"prompt"}`},
		{"memory page", []string{"memory", "get", "large", "--offset", "64000", "--limit", "4000"}, "", "memory.get", `{"path":"large","offset":64000,"limit":4000}`},
		{"schedule list", []string{"schedule", "list"}, "", "schedule.list", `{}`},
		{"schedule cron", []string{"schedule", "create", "--cron", "0 9 * * *", "--prompt", "p"}, "", "schedule.create", `{"cron":"0 9 * * *","prompt":"p"}`},
		{"schedule at", []string{"schedule", "create", "--at", "2026-10-01T09:00:00Z", "--prompt", "p"}, "", "schedule.create", `{"at":"2026-10-01T09:00:00Z","prompt":"p"}`},
		{"schedule delete", []string{"schedule", "delete", "id"}, "", "schedule.delete", `{"id":"id"}`},
		{"notify", []string{"notify", "hello world"}, "ignored", "notify", `{"text":"hello world"}`},
		{"notify stdin", []string{"notify"}, "hello\n", "notify", `{"text":"hello\n"}`},
		{"literal help", []string{"notify", "--", "--help"}, "", "notify", `{"text":"--help"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args, strings.NewReader(tt.stdin))
			if err != nil {
				t.Fatal(err)
			}
			if got.Method != tt.method {
				t.Fatalf("method = %q, want %q", got.Method, tt.method)
			}
			encoded, _ := json.Marshal(got.Data)
			var wantData, gotData any
			_ = json.Unmarshal([]byte(tt.data), &wantData)
			_ = json.Unmarshal(encoded, &gotData)
			if !reflect.DeepEqual(gotData, wantData) {
				t.Fatalf("data = %s, want %s", encoded, tt.data)
			}
		})
	}
}

func TestCreateTraitsFlag(t *testing.T) {
	for _, group := range []string{"agents", "subagents"} {
		for _, flags := range [][]string{nil, {"--traits="}, {"--traits", "Clean code,test-id"}, {"--traits=--yolo"}} {
			req, err := Parse(append([]string{group, "create"}, flags...), strings.NewReader("task"))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(req.Data)
			var data map[string]string
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			got, exists := data["traits"]
			if exists != (flags != nil) {
				t.Fatalf("lost omitted vs empty traits: %s", raw)
			}
			if flags != nil {
				want := strings.TrimPrefix(flags[0], "--traits=")
				if len(flags) == 2 {
					want = flags[1]
				}
				if got != want {
					t.Fatalf("traits = %q, want %q", got, want)
				}
			}
		}
		if _, err := Parse([]string{group, "create", "--traits"}, strings.NewReader("task")); err == nil {
			t.Fatal("missing traits value accepted")
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, args := range [][]string{
		{}, {"unknown"}, {"memory"}, {"memory", "unknown"}, {"agents", "list", "extra"},
		{"memory", "search"}, {"memory", "get", "a", "b"}, {"memory", "set", "--session", "other"},
		{"memory", "set", "--body"}, {"memory", "set", "--body=x", "--body=y"},
		{"memory", "delete"}, {"memory", "delete", "a", "--recursive=maybe"},
		{"agents", "message", "id"}, {"subagents", "create"}, {"notify"},
		{"agents", "create"},
		{"schedule", "create", "--prompt", "p"},
		{"schedule", "create", "--cron", "c", "--at", "a", "--prompt", "p"},
		{"schedule", "create", "--cron=", "--at", "a", "--prompt", "p"},
		{"schedule", "create", "--at", "a"},
		{"schedule", "delete", "--id", "id"},
		{"traits", "get"}, {"traits", "edit"}, {"traits", "list", "extra"},
		{"traits", "edit", "id", "extra"}, {"traits", "get", ""}, {"traits", "edit", " "},
		{"traits", "create"}, {"traits", "delete", "id"}, {"traits", "assign", "id"},
		{"traits", "edit", "id", "--body=x"}, {"traits", "edit", "id", "--name=x"},
		{"traits", "edit", "id", "--default=true"}, {"traits", "get", "id", "--session-id=other"},
	} {
		if _, err := Parse(args, strings.NewReader("")); err == nil {
			t.Errorf("Parse(%q) unexpectedly succeeded", args)
		}
	}
	for _, flag := range []string{"from=other", "session-id=other", "gadget-create-agents=true", "gadget-agents=true", "parent=other", "body=prompt"} {
		if _, err := Parse([]string{"agents", "create", "--" + flag}, strings.NewReader("prompt")); err == nil {
			t.Errorf("creation accepted --%s", flag)
		}
	}
	for _, input := range []string{
		`null`, `{}`, `[null]`, `[{}]`, `[{"path":"a"}]`,
		`[{"path":"a","body":null}]`, `[{"path":1,"body":"b"}]`,
		`[{"path":"a","body":"b","extra":true}]`, `[] []`, `[`,
	} {
		if _, err := Parse([]string{"memory", "batch"}, strings.NewReader(input)); err == nil {
			t.Errorf("batch input %s unexpectedly accepted", input)
		}
	}
}

func TestLocalURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:123/gadgets", "http://[::1]:123/gadgets", "http://localhost:123/gadgets"} {
		if _, err := localURL(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "https://localhost/x", "http://example.com", "http://192.168.1.1", "http://0.0.0.0", "http://localhost.evil", "http://u:p@localhost", "http://localhost/?secret=x", "http://localhost/#fragment", "http://[::1%25lo0]"} {
		if _, err := localURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	u, _ := localURL("http://localhost:123/gadgets")
	if u.String() != "http://127.0.0.1:123/gadgets" {
		t.Fatalf("localhost was not pinned: %v", u)
	}
}

func environment(endpoint string) func(string) string {
	return func(key string) string {
		if key == "JAMES_GADGETS_URL" {
			return endpoint
		}
		if key == "JAMES_GADGETS_TOKEN" {
			return "test-token"
		}
		return ""
	}
}

func TestHTTPProtocol(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/gadgets" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected HTTP request: %s %s %v", r.Method, r.URL, r.Header)
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Method != "memory.batch" {
			t.Errorf("request = %+v, error = %v", req, err)
		}
		_, _ = io.WriteString(w, `{"success":true,"data":{"written":2}}`)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"memory", "batch"}, strings.NewReader(`[{"path":"a","body":"A"},{"path":"b","body":"B"}]`), &stdout, &stderr, environment(server.URL+"/gadgets"), "test")
	if code != 0 || calls != 1 || stderr.Len() != 0 || stdout.String() != "{\"success\":true,\"data\":{\"written\":2}}\n" {
		t.Fatalf("code=%d calls=%d stdout=%s stderr=%s", code, calls, &stdout, &stderr)
	}
}

func TestHTTPFailures(t *testing.T) {
	for _, tt := range []struct {
		name, body, code string
		status           int
	}{
		{"permission", `{"success":false,"error":{"code":"denied","message":"No permission"}}`, "denied", 403},
		{"application", `{"success":false,"error":{"code":"missing","message":"Not found"}}`, "missing", 200},
		{"status", `{"success":true}`, "protocol", 500},
		{"missing success", `{}`, "protocol", 200},
		{"bad type", `{"success":"true"}`, "protocol", 200},
		{"missing error", `{"success":false}`, "protocol", 200},
		{"empty error", `{"success":false,"error":{}}`, "protocol", 200},
		{"inconsistent", `{"success":true,"error":{"code":"x","message":"x"}}`, "protocol", 200},
		{"html", `<html>error</html>`, "protocol", 502},
		{"trailing", `{"success":true} {}`, "protocol", 200},
		{"oversized", strings.Repeat(" ", MaxResponseBytes+1), "protocol", 200},
		{"redirect", `{"success":true}`, "protocol", 302},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/redirect-target")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			code := Run([]string{"agents", "list"}, strings.NewReader(""), &stdout, &stderr, environment(server.URL), "test")
			var result response
			err := json.Unmarshal(stderr.Bytes(), &result)
			if code == 0 || stdout.Len() != 0 || calls != 1 || err != nil || result.Error == nil || result.Error.Code != tt.code {
				t.Fatalf("code=%d calls=%d stdout=%s stderr=%s", code, calls, &stdout, &stderr)
			}
		})
	}
}

func TestBoundsAndConfiguration(t *testing.T) {
	if _, err := Parse([]string{"memory", "set"}, strings.NewReader(strings.Repeat("x", MaxRequestBytes+1))); err == nil {
		t.Fatal("oversized stdin accepted")
	}
	var stdout, stderr bytes.Buffer
	// JSON escaping can push an otherwise bounded stdin past the request limit.
	code := Run([]string{"memory", "set"}, strings.NewReader(strings.Repeat("\x00", MaxRequestBytes/2)), &stdout, &stderr, environment("http://127.0.0.1:1"), "test")
	if code == 0 || !strings.Contains(stderr.String(), `"code":"request_too_large"`) {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
	for _, env := range []func(string) string{
		func(string) string { return "" },
		func(key string) string {
			if key == "JAMES_GADGETS_URL" {
				return "http://127.0.0.1:1"
			}
			return ""
		},
	} {
		stderr.Reset()
		if Run([]string{"agents", "list"}, strings.NewReader(""), &stdout, &stderr, env, "test") == 0 || !strings.Contains(stderr.String(), `"code":"configuration"`) {
			t.Fatalf("expected configuration error: %s", &stderr)
		}
	}
}

func TestHelpVersion(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"--help"}, {"memory", "--help"}, {"memory", "set", "--help"}, {"traits", "--help"}, {"traits", "list", "--help"}, {"traits", "get", "--help"}, {"traits", "edit", "--help"}, {"version"}, {"--version"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, strings.NewReader(""), &stdout, &stderr, func(string) string { return "" }, "1.78.0"); code != 0 || stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("%q: code=%d stdout=%s stderr=%s", args, code, &stdout, &stderr)
		}
	}

}

type failingIO struct{}

func (failingIO) Read([]byte) (int, error)  { return 0, errors.New("read failed") }
func (failingIO) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestStdinAndOutputFailures(t *testing.T) {
	if _, err := Parse([]string{"memory", "set"}, failingIO{}); err == nil {
		t.Fatal("stdin error was discarded")
	}
	if _, err := Parse([]string{"memory", "set", "--body", "explicit"}, failingIO{}); err != nil {
		t.Fatalf("--body must not read stdin: %v", err)
	}
	if _, err := Parse([]string{"traits", "edit", "id"}, failingIO{}); err == nil {
		t.Fatal("trait edit discarded stdin error")
	}
	if _, err := Parse([]string{"traits", "get", "id"}, failingIO{}); err != nil {
		t.Fatalf("trait get must not read stdin: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	defer server.Close()
	var stderr bytes.Buffer
	if Run([]string{"agents", "list"}, failingIO{}, failingIO{}, &stderr, environment(server.URL), "test") == 0 || !strings.Contains(stderr.String(), `"code":"output"`) {
		t.Fatalf("output failure not reported: %s", &stderr)
	}
}

func TestTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	var stdout, stderr bytes.Buffer
	if Run([]string{"agents", "list"}, failingIO{}, &stdout, &stderr, environment(server.URL), "test") == 0 || !strings.Contains(stderr.String(), `"code":"transport"`) {
		t.Fatalf("transport failure not reported: %s", &stderr)
	}
}

func TestLiteralHelpIsNotHelp(t *testing.T) {
	for _, args := range [][]string{{"notify", "--", "--help"}, {"memory", "set", "--body", "--help"}} {
		var stdout, stderr bytes.Buffer
		if Run(args, failingIO{}, &stdout, &stderr, func(string) string { return "" }, "test") == 0 || !strings.Contains(stderr.String(), `"code":"configuration"`) {
			t.Fatalf("%q interpreted as help: stdout=%s stderr=%s", args, &stdout, &stderr)
		}
	}
}
