package web

import (
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"

	"james/moneypenny/pkg/envelope"
)

const maxPushDevices = 16

// Chrome's FCM endpoint is the only outbound destination accepted from a browser.
var chromePushEndpoint = regexp.MustCompile(`^https://fcm\.googleapis\.com/fcm/send/[A-Za-z0-9_:-]+$`)

type pushState struct {
	Binding       string
	PrivateKey    string
	PublicKey     string
	Subscriptions map[string]webpush.Subscription
}

type webPush struct {
	path   string
	mu     sync.Mutex
	state  pushState
	client *http.Client
}

// EnableWebPush loads persistent device subscriptions before the server starts.
func (s *Server) EnableWebPush(path string) error {
	if s.password == "" {
		return errors.New("Web Push requires Qew authentication")
	}
	p := &webPush{path: path, client: &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte("qew web push"))
	binding := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 128<<10 {
			return errors.New("invalid or insecure Qew push store")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &p.state); err != nil {
			return fmt.Errorf("decode Qew push store: %w", err)
		}
	case !os.IsNotExist(err):
		return err
	}
	if p.state.Binding != binding {
		private, public, err := webpush.GenerateVAPIDKeys()
		if err != nil {
			return err
		}
		if err := p.save(pushState{binding, private, public, make(map[string]webpush.Subscription)}); err != nil {
			return err
		}
	}
	if p.state.PrivateKey == "" || p.state.PublicKey == "" || p.state.Subscriptions == nil || len(p.state.Subscriptions) > maxPushDevices {
		return errors.New("invalid Qew push state")
	}
	for endpoint, sub := range p.state.Subscriptions {
		if endpoint != sub.Endpoint || !validPushSubscription(sub) {
			return errors.New("invalid stored Qew push subscription")
		}
	}
	s.push = p
	return nil
}

func (p *webPush) save(state pushState) error {
	f, err := os.CreateTemp(filepath.Dir(p.path), ".qew-push-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	encodeErr := json.NewEncoder(f).Encode(state)
	closeErr := f.Close()
	if err := errors.Join(encodeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), p.path); err != nil {
		return err
	}
	p.state = state
	return nil
}

func validPushSubscription(sub webpush.Subscription) bool {
	if len(sub.Endpoint) > 2048 || !chromePushEndpoint.MatchString(sub.Endpoint) {
		return false
	}
	auth, err := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return false
	}
	key, err := base64.RawURLEncoding.DecodeString(sub.Keys.P256dh)
	if err != nil {
		return false
	}
	_, err = ecdh.P256().NewPublicKey(key)
	return err == nil
}

func (p *webPush) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		p.mu.Lock()
		key := p.state.PublicKey
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, string(mustJSON(map[string]string{"publicKey": key})))
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var sub webpush.Subscription
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil ||
		!chromePushEndpoint.MatchString(sub.Endpoint) || (r.Method == http.MethodPost && !validPushSubscription(sub)) {
		http.Error(w, "Invalid subscription; background notifications require Chrome.", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.state
	old, exists := next.Subscriptions[sub.Endpoint]
	if (r.Method == http.MethodPost && exists && old == sub) || (r.Method == http.MethodDelete && !exists) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodPost && !exists && len(next.Subscriptions) >= maxPushDevices {
		http.Error(w, "Background notification device limit reached.", http.StatusConflict)
		return
	}
	next.Subscriptions = maps.Clone(next.Subscriptions)
	if r.Method == http.MethodPost {
		next.Subscriptions[sub.Endpoint] = sub
	} else {
		delete(next.Subscriptions, sub.Endpoint)
	}
	if err := p.save(next); err != nil {
		log.Printf("Qew push: save devices: %v", err)
		http.Error(w, "Could not save background notification settings.", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type pushSession struct {
	SessionID string                     `json:"session_id"`
	ParentID  string                     `json:"parent_session_id"`
	Execution string                     `json:"execution"`
	Activity  string                     `json:"last_activity"`
	Unread    bool                       `json:"unread"`
	Completed bool                       `json:"completed"`
	Attention *envelope.SessionAttention `json:"attention"`
}

type pushAlert struct {
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`
}

func pushChanges(previous map[string]pushSession, sessions []pushSession) (map[string]pushSession, []pushAlert) {
	next := make(map[string]pushSession, len(sessions))
	var alerts []pushAlert
	for _, session := range sessions {
		id, err := uuid.Parse(session.SessionID)
		if err != nil || id.String() != session.SessionID || session.ParentID != "" || session.Completed {
			continue
		}
		old, seen := previous[session.SessionID]
		if session.Execution == "offline" || session.Execution == "unknown" {
			if seen {
				next[session.SessionID] = old
			}
			continue
		}
		next[session.SessionID] = session
		if previous == nil {
			continue
		}
		if session.Attention.Active() && (!old.Attention.Active() || session.Attention.Revision > old.Attention.Revision) {
			alerts = append(alerts, pushAlert{session.SessionID, "attention"})
		} else if session.Unread && (session.Execution == "idle" || session.Execution == "ready") &&
			(!seen || !old.Unread || old.Execution == "working" || session.Activity != old.Activity) {
			alerts = append(alerts, pushAlert{session.SessionID, "result"})
		}
	}
	return next, alerts
}

func (p *webPush) run(ctx context.Context, hem HemClient) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var previous map[string]pushSession
	for {
		p.mu.Lock()
		state := p.state // snapshots are immutable after publication by save
		p.mu.Unlock()
		if len(state.Subscriptions) == 0 {
			previous = nil
		} else if sessions, err := pushDashboard(hem); err != nil {
			log.Printf("Qew push: %v", err)
		} else {
			var alerts []pushAlert
			previous, alerts = pushChanges(previous, sessions)
			for _, alert := range alerts {
				p.deliver(ctx, state, alert)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func pushDashboard(hem HemClient) ([]pushSession, error) {
	resp, err := hem.Send(&Request{Verb: "dashboard", Args: []string{"--all", "--view=workstreams"}})
	if err != nil {
		return nil, fmt.Errorf("dashboard request failed: %w", err)
	}
	if resp.Status != "ok" {
		return nil, errors.New("Hem rejected the workstream dashboard request")
	}
	var data struct {
		Workstreams *[]struct{ Sessions []pushSession } `json:"workstreams"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil || data.Workstreams == nil {
		return nil, errors.New("invalid workstream dashboard response")
	}
	var sessions []pushSession
	for _, stream := range *data.Workstreams {
		sessions = append(sessions, stream.Sessions...)
	}
	return sessions, nil
}

func (p *webPush) deliver(ctx context.Context, state pushState, alert pushAlert) {
	payload := mustJSON(alert)
	options := &webpush.Options{
		HTTPClient: p.client, VAPIDPublicKey: state.PublicKey, VAPIDPrivateKey: state.PrivateKey,
		Subscriber: "https://github.com/cfe84/james", TTL: 300,
	}
	var devices sync.WaitGroup
	for _, sub := range state.Subscriptions {
		devices.Go(func() { p.deliverToDevice(ctx, payload, sub, options) })
	}
	devices.Wait()
}

func (p *webPush) deliverToDevice(ctx context.Context, payload []byte, sub webpush.Subscription, options *webpush.Options) {
	p.mu.Lock()
	current := p.state.Subscriptions[sub.Endpoint]
	p.mu.Unlock()
	if current != sub || ctx.Err() != nil {
		return
	}
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &sub, options)
	if err != nil {
		// Transport errors can contain the private subscription URL.
		log.Print("Qew push: delivery request failed")
		return
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusGone:
		p.removeExpired(sub)
	default:
		if resp.StatusCode/100 != 2 {
			log.Printf("Qew push: provider returned HTTP %d", resp.StatusCode)
		}
	}
}

func (p *webPush) removeExpired(sub webpush.Subscription) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Subscriptions[sub.Endpoint] != sub {
		return
	}
	next := p.state
	next.Subscriptions = maps.Clone(next.Subscriptions)
	delete(next.Subscriptions, sub.Endpoint)
	if err := p.save(next); err != nil {
		log.Printf("Qew push: remove expired device: %v", err)
	}
}
