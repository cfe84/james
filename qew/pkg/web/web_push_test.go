package web

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"james/moneypenny/pkg/envelope"
)

const pushTestSession = "12345678-1234-1234-1234-123456789abc"

func TestPushObservesNewTopLevelResultsAndAttention(t *testing.T) {
	root := pushSession{SessionID: pushTestSession, Execution: "idle", Unread: true, Activity: "old"}
	child := pushSession{SessionID: "22345678-1234-1234-1234-123456789abc", ParentID: root.SessionID, Execution: "idle"}
	previous, alerts := pushChanges(nil, []pushSession{root, child})
	if len(alerts) != 0 || len(previous) != 1 {
		t.Fatalf("unexpected initial alerts/state: %v %v", alerts, previous)
	}
	for _, step := range []struct {
		execution string
		activity  string
		revision  int64
		want      string
	}{
		{"working", "new", 0, ""},
		{"offline", "", 0, ""},
		{"idle", "new", 0, "result"},
		{"idle", "new", 1, "attention"},
		{"idle", "new", 1, ""},
		{"idle", "new", 3, "attention"},
	} {
		root.Execution, root.Activity = step.execution, step.activity
		if step.revision != 0 {
			root.Attention = &envelope.SessionAttention{Kind: "needs_answer", Revision: step.revision}
		}
		child.Attention = &envelope.SessionAttention{Kind: "needs_answer", Revision: step.revision + 1}
		previous, alerts = pushChanges(previous, []pushSession{root, child})
		if step.want == "" && len(alerts) != 0 || step.want != "" &&
			(len(alerts) != 1 || alerts[0] != (pushAlert{root.SessionID, step.want})) {
			t.Fatalf("step %+v: got %v", step, alerts)
		}
	}
}

type pushTransport func(*http.Request) (*http.Response, error)

func (f pushTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebPushRegistrationAndDeliveryLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qew_push.json")
	open := func(password string) *Server {
		s := NewServer(nil, "", password, false, nil, "", []byte("test-seed"), nil)
		t.Cleanup(s.Close)
		if err := s.EnableWebPush(path); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open("password")
	mux, err := s.routes()
	if err != nil {
		t.Fatal(err)
	}
	device, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub := webpush.Subscription{
		Endpoint: "https://fcm.googleapis.com/fcm/send/test-token",
		Keys: webpush.Keys{
			Auth:   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
			P256dh: base64.RawURLEncoding.EncodeToString(device.PublicKey().Bytes()),
		},
	}
	send := func(sub webpush.Subscription, login, csrf bool) int {
		r := httptest.NewRequest(http.MethodPost, "/web-push", strings.NewReader(string(mustJSON(sub))))
		if login {
			now := time.Now()
			r.AddCookie(s.sessionCookie(s.makeToken(now, now)))
		}
		if csrf {
			r.Header.Set("X-Requested-With", "QewClient")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	invalid := sub
	invalid.Endpoint = "https://fcm.googleapis.com@127.0.0.1/fcm/send/test-token"
	for _, tc := range []struct {
		sub         webpush.Subscription
		login, csrf bool
		want        int
	}{
		{sub, false, true, http.StatusFound},
		{sub, true, false, http.StatusForbidden},
		{invalid, true, true, http.StatusBadRequest},
		{sub, true, true, http.StatusNoContent},
	} {
		if got := send(tc.sub, tc.login, tc.csrf); got != tc.want {
			t.Fatalf("registration status %d; want %d", got, tc.want)
		}
	}
	restored := open("password").push
	if restored.state.PublicKey != s.push.state.PublicKey || restored.state.Subscriptions[sub.Endpoint] != sub {
		t.Fatal("device or VAPID identity did not survive restart")
	}
	delivered := false
	restored.client.Transport = pushTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) == 0 || strings.Contains(string(body), pushTestSession) ||
			r.URL.String() != sub.Endpoint || r.Header.Get("Content-Encoding") != "aes128gcm" ||
			!strings.HasPrefix(r.Header.Get("Authorization"), "vapid ") {
			t.Errorf("expected encrypted, VAPID-authenticated request: body length %d, error %v", len(body), err)
		}
		delivered = true
		return &http.Response{StatusCode: http.StatusGone, Body: http.NoBody}, nil
	})
	restored.deliver(context.Background(), restored.state, pushAlert{pushTestSession, "result"})
	if !delivered || len(open("password").push.state.Subscriptions) != 0 {
		t.Fatal("expired device was not removed persistently")
	}
	s = open("password")
	mux, err = s.routes()
	if err != nil {
		t.Fatal(err)
	}
	if send(sub, true, true) != http.StatusNoContent {
		t.Fatal("could not re-register device")
	}
	replaced := open("new-password").push
	if len(replaced.state.Subscriptions) != 0 || replaced.state.PublicKey == restored.state.PublicKey {
		t.Fatal("password rotation did not revoke subscriptions and rotate VAPID keys")
	}
}
