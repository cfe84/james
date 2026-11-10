package channel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type teamsTestTransport func(*http.Request) (*http.Response, error)

func (f teamsTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func nativeTeamsTestClient(server *httptest.Server) *http.Client {
	return &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}, Transport: teamsTestTransport(func(req *http.Request) (*http.Response, error) {
		local := req.Clone(req.Context())
		address := *req.URL
		base, _ := url.Parse(server.URL)
		address.Scheme, address.Host = base.Scheme, base.Host
		local.URL = &address
		return server.Client().Transport.RoundTrip(local)
	})}
}

func TestNativeTeamsRegionalRedirect(t *testing.T) {
	for _, tc := range []struct {
		name, location string
		wantRequests   int32
		wantError      bool
	}{
		{"regional", "https://teams.cloud.microsoft/api/chatsvc/emea/v1/users/ME/conversations/chat/messages", 3, false},
		{"other origin", "https://evil.example/api/chatsvc/emea", 1, true},
		{"credentials", "https://user@teams.cloud.microsoft/api/chatsvc/emea", 1, true},
		{"other service", "https://teams.cloud.microsoft/api/other/emea", 1, true},
		{"redirect loop", teamsChatBaseURL, 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("BehaviorOverride") != "redirectAs404" {
					t.Error("missing direct ChatSvc authentication or geo routing header")
				}
				if strings.HasPrefix(r.URL.Path, "/api/chatsvc/noam-canary/") {
					w.Header().Set("Location", tc.location)
					w.WriteHeader(http.StatusNotFound)
				} else if r.URL.Query().Get("page") == "2" {
					fmt.Fprint(w, `{"messages":[]}`)
				} else {
					fmt.Fprintf(w, `{"messages":[{"id":"new","originalarrivaltime":"2026-11-10T12:00:01Z","messagetype":"Text","content":"hello"}],"_metadata":{"backwardLink":%q}}`, r.URL.Path+"?page=2")
				}
			}))
			defer server.Close()
			p := &nativeTeams{http: nativeTeamsTestClient(server), base: teamsChatBaseURL, bearer: "secret"}
			messages, err := p.ListMessages(context.Background(), "chat", "2026-11-10T12:00:00Z")
			if (err != nil) != tc.wantError || requests.Load() != tc.wantRequests {
				t.Fatalf("redirect: messages=%+v err=%v requests=%d", messages, err, requests.Load())
			}
			if !tc.wantError && (len(messages) != 1 || messages[0].ID != "new" ||
				p.base != "https://teams.cloud.microsoft/api/chatsvc/emea") {
				t.Fatalf("regional catch-up failed: %+v base=%s", messages, p.base)
			}
		})
	}
}

func TestNativeTeamsPollingReceiveReplyAndCredentialReset(t *testing.T) {
	const chat = "19:test@thread.v2"
	var sent atomic.Bool
	var rejectSend atomic.Bool
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"oid":"owner"}`))
	token := "test." + claims + ".signature"
	prefix := "/api/chatsvc/noam-canary"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Skypetoken") != "" {
			t.Errorf("incorrect chat credential on %s", r.URL.Path)
		}
		switch r.URL.Path {
		case prefix + "/v1/users/ME/conversations":
			fmt.Fprintf(w, `{"conversations":[{"id":%q,"threadProperties":{"topic":"Test chat"}}]}`, chat)
		case prefix + "/v1/users/ME/conversations/" + chat:
			fmt.Fprintf(w, `{"id":%q,"threadProperties":{"topic":"Test chat"}}`, chat)
		case prefix + "/v1/users/ME/conversations/" + chat + "/messages",
			"/api/chatsvc/emea/v1/users/ME/conversations/" + chat + "/messages":
			if r.Method == http.MethodPost {
				if rejectSend.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["messagetype"] != "RichText/Html" || !strings.Contains(body["content"], "<b>Done</b>") ||
					!strings.Contains(body["content"], "Ian") || body["clientmessageid"] == "" {
					t.Errorf("invalid outbound message: %+v", body)
				}
				if strings.HasPrefix(r.URL.Path, prefix+"/") {
					w.Header().Set("Location", "https://teams.cloud.microsoft/api/chatsvc/emea")
					w.WriteHeader(http.StatusNotFound)
					return
				}
				sent.Store(true)
				w.Header().Set("Location", r.URL.Path+"/reply")
				w.WriteHeader(http.StatusCreated)
				return
			}
			if r.URL.Query().Get("startTime") == "" {
				fmt.Fprint(w, `{"messages":[{"id":"old","originalarrivaltime":"2026-11-10T12:00:00Z","from":"8:orgid:owner","messagetype":"Text","content":"old"}]}`)
			} else if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `{"messages":[{"id":"old","originalarrivaltime":"2026-11-10T12:00:00Z","from":"8:orgid:owner","messagetype":"Text","content":"old"}]}`)
			} else {
				fmt.Fprintf(w, `{"messages":[{"id":"new","originalarrivaltime":"2026-11-10T12:00:01.1234567Z","from":"https://amer.ng.msg.teams.microsoft.com/v1/users/ME/contacts/8:orgid:owner","imdisplayname":"Owner","messagetype":"RichText/Html","content":"<b>Hello</b>"}],"_metadata":{"backwardLink":%q}}`, r.URL.Path+"?page=2")
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p, err := newNativeTeams(authResult{Token: token, Account: "Owner"}, nativeTeamsTestClient(server))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, name, err := p.Self(ctx)
	if err != nil || id != "8:orgid:owner" || name != "Owner" {
		t.Fatalf("invalid owner identity: %q %q %v", id, name, err)
	}
	targets, err := p.Search(ctx, "test")
	if err != nil || len(targets) != 1 || targets[0].ID != chat {
		t.Fatalf("search: %+v %v", targets, err)
	}
	if target, err := p.Resolve(ctx, chat); err != nil || target.Label != "Test chat" {
		t.Fatalf("resolve: %+v %v", target, err)
	}
	_, cursor, err := p.LatestCursor(ctx, chat)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := p.ListMessages(ctx, chat, cursor)
	if err != nil || len(messages) != 1 || messages[0].ID != "new" ||
		messages[0].Text != "Hello" || messages[0].SenderID != id {
		t.Fatalf("received messages: %+v %v", messages, err)
	}
	raw, _ := json.Marshal(map[string]string{"target_id": chat, "sender_name": "Ian", "content": "**Done**"})
	result, err := runPluginMethod(ctx, p, "send", raw)
	if err != nil || result.(map[string]string)["id"] != "reply" || !sent.Load() {
		t.Fatalf("reply: %+v %v", result, err)
	}
	rejectSend.Store(true)
	plugin := newTrouterPlugin(false, "")
	plugin.authState, plugin.token = "authenticated", p.bearer
	plugin.provider = p
	if _, err := plugin.handle(ctx, "send", raw); err == nil {
		t.Fatal("authentication failure was accepted")
	}
	if status := plugin.status(); status["auth"] != "not_started" || status["connection"] != "disconnected" || plugin.token != "" {
		t.Fatalf("rejected credentials were retained: %+v", status)
	}
}

func TestNativeTeamsRejectsUnsafeOrFailedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"HTTP failure", `{"messages":[]}`, http.StatusUnauthorized},
		{"application failure", `{"errorCode":"Denied","messages":[]}`, http.StatusOK},
		{"missing messages", `{}`, http.StatusOK},
		{"invalid timestamp", `{"messages":[{"id":"x","originalarrivaltime":"bad"}]}`, http.StatusOK},
		{"cross-origin pagination", `{"messages":[{"id":"x","originalarrivaltime":"2026-11-10T12:00:01Z"}],"_metadata":{"backwardLink":"https://evil.example/steal"}}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p := &nativeTeams{http: nativeTeamsTestClient(server), base: teamsChatBaseURL, bearer: "secret"}
			_, err := p.ListMessages(context.Background(), "chat", "2026-11-10T12:00:00Z")
			if err == nil || requests.Load() != 1 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe response was accepted: %v, requests=%d", err, requests.Load())
			}
		})
	}
}
