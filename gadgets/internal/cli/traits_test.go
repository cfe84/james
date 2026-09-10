package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTraitCommandResultsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		wantCode     int
	}{
		{"updated", `{"success":true,"data":{"id":"existing","name":"Trait","prompt":" \nbody\n","enabled_by_default":true}}`, 0},
		{"denied", `{"success":false,"error":{"code":"permission_denied","message":"Capability is disabled"}}`, 1},
		{"not found", `{"success":false,"error":{"code":"operation_failed","message":"trait not found"}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req Request
				if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "traits.edit" || len(req.Data) != 2 ||
					req.Data["id"] != "existing" || req.Data["body"] != " \nbody\n" {
					t.Errorf("incorrect trait request: %+v", req)
				}
				_, _ = io.WriteString(w, tc.result)
			}))
			defer server.Close()
			var out, errOut bytes.Buffer
			code := Run([]string{"traits", "edit", "existing"}, strings.NewReader(" \nbody\n"), &out, &errOut, environment(server.URL), "test")
			got := strings.TrimSpace(out.String())
			if tc.wantCode != 0 {
				got = strings.TrimSpace(errOut.String())
			}
			if code != tc.wantCode || calls != 1 || got != tc.result ||
				(tc.wantCode == 0 && errOut.Len() != 0) || (tc.wantCode != 0 && out.Len() != 0) {
				t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, &out, &errOut)
			}
		})
	}
	if _, err := Parse([]string{"traits", "edit", "id"}, strings.NewReader(strings.Repeat("x", MaxRequestBytes+1))); err == nil {
		t.Fatal("oversized trait stdin accepted")
	}
	for _, text := range []string{"gadgets traits list", "gadgets traits get ID", "gadgets traits edit ID", "default: false", "future use by all agents"} {
		if !strings.Contains(Help, text) {
			t.Fatalf("missing documented contract: %s", text)
		}
	}
}
