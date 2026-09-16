package handler

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"james/moneypenny/pkg/envelope"
)

const (
	watchRenewalInterval = 20 * time.Second
	watchLeaseDuration   = 60 * time.Second
)

type sessionWatch struct {
	WatchID      string
	ConnectionID string
	Epoch        uint64
	SessionID    string
	ExpiresAt    time.Time
}

type watchRegistry struct {
	mu      sync.Mutex
	now     func() time.Time
	watches map[string]sessionWatch
	stop    chan struct{}
	done    chan struct{}
	running bool
	closed  bool
}

func newWatchRegistry() *watchRegistry {
	return &watchRegistry{now: time.Now, watches: make(map[string]sessionWatch)}
}

func (r *watchRegistry) Close() {
	r.mu.Lock()
	r.closed = true
	stop, done, running := r.stop, r.done, r.running
	r.mu.Unlock()
	if running {
		select {
		case <-stop:
		default:
			close(stop)
		}
		<-done
	}
}

func (r *watchRegistry) expiryLoop(stop <-chan struct{}, done chan<- struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			r.mu.Lock()
			r.expireLocked(r.now())
			if len(r.watches) == 0 {
				r.running = false
				r.mu.Unlock()
				close(done)
				return
			}
			r.mu.Unlock()
		case <-stop:
			r.mu.Lock()
			r.running = false
			r.mu.Unlock()
			close(done)
			return
		}
	}
}

func (r *watchRegistry) open(w sessionWatch) (sessionWatch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.now())
	if r.closed {
		return sessionWatch{}, fmt.Errorf("watch registry closed")
	}
	if old, ok := r.watches[w.WatchID]; ok {
		if old.ConnectionID != w.ConnectionID || old.Epoch != w.Epoch || old.SessionID != w.SessionID {
			return sessionWatch{}, fmt.Errorf("watch belongs to another connection epoch")
		}
		old.ExpiresAt = r.now().Add(watchLeaseDuration)
		r.watches[w.WatchID] = old
		return old, nil
	}
	w.ExpiresAt = r.now().Add(watchLeaseDuration)
	r.watches[w.WatchID] = w
	if !r.running {
		r.running = true
		r.stop = make(chan struct{})
		r.done = make(chan struct{})
		go r.expiryLoop(r.stop, r.done)
	}
	return w, nil
}

func (r *watchRegistry) renew(id, connection string, epoch uint64) (sessionWatch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.now())
	w, ok := r.watches[id]
	if !ok || w.ConnectionID != connection || w.Epoch != epoch {
		return sessionWatch{}, fmt.Errorf("stale or unknown watch lease")
	}
	w.ExpiresAt = r.now().Add(watchLeaseDuration)
	r.watches[id] = w
	return w, nil
}

func (r *watchRegistry) close(id, connection string, epoch uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.watches[id]
	if !ok || w.ConnectionID != connection || w.Epoch != epoch {
		return false
	}

	delete(r.watches, id)
	return true
}

func (r *watchRegistry) closeSession(session string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.watches {
		if w.SessionID == session {
			delete(r.watches, id)
		}
	}
}

func (r *watchRegistry) expireLocked(now time.Time) {
	for id, w := range r.watches {
		if !now.Before(w.ExpiresAt) {
			delete(r.watches, id)
		}
	}
}

func watchPayload(cmdData json.RawMessage) (sessionWatch, error) {
	var p struct {
		WatchID      string `json:"watch_id"`
		ConnectionID string `json:"connection_id"`
		Epoch        uint64 `json:"epoch"`
		SessionID    string `json:"session_id"`
	}
	if err := json.Unmarshal(cmdData, &p); err != nil {
		return sessionWatch{}, fmt.Errorf("decode watch: %w", err)
	}
	if p.WatchID == "" || p.ConnectionID == "" || p.Epoch == 0 || p.SessionID == "" {
		return sessionWatch{}, fmt.Errorf("watch_id, connection_id, epoch, and session_id are required")
	}
	return sessionWatch{WatchID: p.WatchID, ConnectionID: p.ConnectionID, Epoch: p.Epoch, SessionID: p.SessionID}, nil
}

func (h *Handler) watchSession(cmdData json.RawMessage, requestID string) *envelope.Response {
	w, err := watchPayload(cmdData)
	if err != nil {
		return envelope.ErrorResponse(requestID, envelope.ErrInvalidRequest, err.Error())
	}
	w, err = h.watches.open(w)
	if err != nil {
		return envelope.ErrorResponse(requestID, envelope.ErrInvalidRequest, err.Error())
	}
	return envelope.SuccessResponse(requestID, map[string]interface{}{
		"watch_id": w.WatchID, "connection_id": w.ConnectionID, "epoch": w.Epoch,
		"session_id": w.SessionID, "expires_at": w.ExpiresAt,
	})
}

func (h *Handler) renewWatch(cmdData json.RawMessage, requestID string) *envelope.Response {
	w, err := watchPayload(cmdData)
	if err != nil {
		return envelope.ErrorResponse(requestID, envelope.ErrInvalidRequest, err.Error())
	}
	w, err = h.watches.renew(w.WatchID, w.ConnectionID, w.Epoch)
	if err != nil {
		return envelope.ErrorResponse(requestID, envelope.ErrInvalidRequest, err.Error())
	}
	return envelope.SuccessResponse(requestID, map[string]interface{}{
		"watch_id": w.WatchID, "connection_id": w.ConnectionID, "epoch": w.Epoch,
		"session_id": w.SessionID, "expires_at": w.ExpiresAt,
	})
}

func (h *Handler) unwatchSession(cmdData json.RawMessage, requestID string) *envelope.Response {
	w, err := watchPayload(cmdData)
	if err != nil {
		return envelope.ErrorResponse(requestID, envelope.ErrInvalidRequest, err.Error())
	}
	closed := h.watches.close(w.WatchID, w.ConnectionID, w.Epoch)
	return envelope.SuccessResponse(requestID, map[string]interface{}{"closed": closed})
}
