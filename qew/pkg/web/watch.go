package web

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

const (
	qewWatchRenewalInterval = 20 * time.Second
	qewWatchLeaseDuration   = 60 * time.Second
)

type qewWatch struct {
	id, connection, session string
	epoch                   uint64
	expires                 time.Time
}

type qewWatchGroup struct {
	session       string
	upstreamID    string
	upstreamConn  string
	upstreamEpoch uint64
	consumers     map[string]qewWatch
}

type watchHub struct {
	hem             HemClient
	mu              sync.Mutex
	openMu          sync.Mutex
	groups          map[string]*qewWatchGroup
	byID            map[string]string
	now             func() time.Time
	renewalInterval time.Duration
	leaseDuration   time.Duration
	stop            chan struct{}
	done            chan struct{}
	renewing        bool
	closed          bool
}

func randomWatchID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%x", prefix, b), nil
}

func nextWatchConnection() (string, uint64, error) {
	id, err := randomWatchID("qew-connection")
	if err != nil {
		return "", 0, err
	}
	var b [8]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", 0, err
	}
	epoch := uint64(0)
	for _, v := range b {
		epoch = (epoch << 8) | uint64(v)
	}
	if epoch == 0 {
		epoch = 1
	}
	return id, epoch, nil
}

func newWatchHub(hem HemClient) *watchHub {
	h := &watchHub{hem: hem, groups: make(map[string]*qewWatchGroup), byID: make(map[string]string), now: time.Now, renewalInterval: qewWatchRenewalInterval, leaseDuration: qewWatchLeaseDuration}
	return h
}

func (h *watchHub) open(connection string, epoch uint64, session string) (qewWatch, error) {
	if connection == "" || epoch == 0 || session == "" {
		return qewWatch{}, fmt.Errorf("invalid watch identity")
	}
	h.openMu.Lock()
	defer h.openMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return qewWatch{}, fmt.Errorf("watch hub closed")
	}
	duration := h.leaseDuration
	if duration <= 0 {
		duration = qewWatchLeaseDuration
	}
	h.expireLocked(h.now())
	id, err := randomWatchID("qew-watch")
	if err != nil {
		h.mu.Unlock()
		return qewWatch{}, err
	}
	g := h.groups[session]
	first := g == nil
	if first {
		upstreamID, err := randomWatchID("qew-upstream")
		if err != nil {
			h.mu.Unlock()
			return qewWatch{}, err
		}
		_, upstreamEpoch, err := nextWatchConnection()
		if err != nil {
			h.mu.Unlock()
			return qewWatch{}, err
		}
		g = &qewWatchGroup{session: session, upstreamID: upstreamID, upstreamConn: upstreamID, upstreamEpoch: upstreamEpoch, consumers: make(map[string]qewWatch)}
		h.groups[session] = g
	}
	w := qewWatch{id: id, connection: connection, session: session, epoch: epoch, expires: h.now().Add(duration)}
	if first {
		h.mu.Unlock()
		if _, err := h.hem.Send(&Request{Verb: "watch", Noun: "lease", Args: watchArgs(g.upstreamID, g.upstreamConn, g.upstreamEpoch, session)}); err != nil {
			h.mu.Lock()
			delete(h.groups, session)
			h.mu.Unlock()
			return qewWatch{}, err
		}
		h.mu.Lock()
		g = h.groups[session]
	}
	g.consumers[id] = w
	if !h.renewing {
		h.renewing = true
		h.stop = make(chan struct{})
		h.done = make(chan struct{})
		go h.renewLoop(h.stop, h.done)
	}
	h.byID[id] = session
	h.mu.Unlock()
	return w, nil
}

func watchArgs(id, connection string, epoch uint64, session string) []string {
	return []string{"--watch-id", id, "--connection-id", connection, "--epoch", fmt.Sprint(epoch), "--session-id", session}
}

func (h *watchHub) renew(id, connection string, epoch uint64) (qewWatch, error) {
	h.mu.Lock()
	h.expireLocked(h.now())
	session, ok := h.byID[id]
	g := h.groups[session]
	if !ok || g == nil {
		h.mu.Unlock()
		return qewWatch{}, fmt.Errorf("stale or unknown watch")
	}
	w, exists := g.consumers[id]
	if !exists || w.connection != connection || w.epoch != epoch {
		h.mu.Unlock()
		return qewWatch{}, fmt.Errorf("stale or unknown watch")
	}
	duration := h.leaseDuration
	if duration <= 0 {
		duration = qewWatchLeaseDuration
	}
	w.expires = h.now().Add(duration)
	g.consumers[id] = w
	h.mu.Unlock()
	return w, nil
}

func (h *watchHub) close(id, connection string, epoch uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closeLocked(id, connection, epoch)
}

func (h *watchHub) closeLocked(id, connection string, epoch uint64) bool {
	session, ok := h.byID[id]
	if !ok {
		return false
	}
	g := h.groups[session]
	w, exists := g.consumers[id]
	if !exists || w.connection != connection || w.epoch != epoch {
		return false
	}
	delete(g.consumers, id)
	delete(h.byID, id)
	if len(g.consumers) == 0 {
		delete(h.groups, session)
		go h.sendUnwatch(g)
	}
	return true
}

func (h *watchHub) releaseConnection(connection string, epoch uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, session := range h.byID {
		g := h.groups[session]
		if w, ok := g.consumers[id]; ok && w.connection == connection && w.epoch == epoch {
			h.closeLocked(id, connection, epoch)
		}
	}
}

func (h *watchHub) renewLocal(connection string, epoch uint64) {
	h.mu.Lock()
	h.expireLocked(h.now())
	for id, session := range h.byID {
		if w, ok := h.groups[session].consumers[id]; ok && w.connection == connection && w.epoch == epoch {
			w.expires = h.now().Add(h.leaseDuration)
			h.groups[session].consumers[id] = w
		}
	}
	h.mu.Unlock()
}

func (h *watchHub) expireLocked(now time.Time) {
	for id, session := range h.byID {
		g := h.groups[session]
		if w, ok := g.consumers[id]; ok && !now.Before(w.expires) {
			h.closeLocked(id, w.connection, w.epoch)
		}
	}
}

func (h *watchHub) sendUnwatch(g *qewWatchGroup) {
	_, _ = h.hem.Send(&Request{Verb: "unwatch", Noun: "watch", Args: watchArgs(g.upstreamID, g.upstreamConn, g.upstreamEpoch, g.session)})
}

func (h *watchHub) renewLoop(stop <-chan struct{}, done chan<- struct{}) {
	interval := h.renewalInterval
	if interval <= 0 {
		interval = qewWatchRenewalInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			h.mu.Lock()
			h.renewing = false
			h.mu.Unlock()
			close(done)
			return
		case <-t.C:
			h.mu.Lock()
			h.expireLocked(h.now())
			if len(h.groups) == 0 {
				h.renewing = false
				h.mu.Unlock()
				close(done)
				return
			}
			var groups []*qewWatchGroup
			for _, g := range h.groups {
				if len(g.consumers) > 0 {
					groups = append(groups, g)
				}
			}
			h.mu.Unlock()
			for _, g := range groups {
				_, _ = h.hem.Send(&Request{Verb: "renew", Noun: "watch", Args: watchArgs(g.upstreamID, g.upstreamConn, g.upstreamEpoch, g.session)})
			}
		}
	}
}

func (h *watchHub) closeHub() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	stop, done, running := h.stop, h.done, h.renewing
	h.mu.Unlock()
	if running {
		select {
		case <-stop:
		default:
			close(stop)
		}
		<-done
	}
}
