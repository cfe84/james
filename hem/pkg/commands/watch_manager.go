package commands

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// These are protocol defaults kept in parity with the Qew and Moneypenny
// implementations. Polling remains authoritative; watches only reduce refresh
// latency.
const (
	WatchRenewalInterval = 20 * time.Second
	WatchLeaseDuration   = 60 * time.Second
)

type WatchLease struct {
	WatchID      string    `json:"watch_id"`
	ConnectionID string    `json:"connection_id"`
	Epoch        uint64    `json:"epoch"`
	SessionID    string    `json:"session_id"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func randomWatchIdentity(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%x", prefix, b), nil
}

func randomWatchEpoch() (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	var epoch uint64
	for _, v := range b {
		epoch = epoch<<8 | uint64(v)
	}
	if epoch == 0 {
		epoch = 1
	}
	return epoch, nil
}

func (wm *WatchManager) Aggregate(sessionID string) (WatchLease, bool) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	a, ok := wm.aggregates[sessionID]
	if !ok {
		return WatchLease{}, false
	}
	return a.upstream, true
}

type watchRecord struct {
	WatchLease
}

type watchAggregate struct {
	session  string
	upstream WatchLease
}

// WatchManager owns connection-scoped leases. The session refcount is the
// upstream-watch aggregation boundary: removing one browser consumer cannot
// tear down a watch still used by another consumer.
type WatchManager struct {
	watchers          map[string][]string // legacy parentSessionID -> child IDs
	lastSessionStates map[string]string
	leases            map[string]watchRecord
	sessionRefs       map[string]int
	aggregates        map[string]watchAggregate
	now               func() time.Time
	expireInterval    time.Duration
	onExpire          func(WatchLease)
	stop              chan struct{}
	done              chan struct{}
	running           bool
	closed            bool
	mu                sync.Mutex
}

func NewWatchManager() *WatchManager {
	wm := &WatchManager{
		watchers:          make(map[string][]string),
		lastSessionStates: make(map[string]string),
		leases:            make(map[string]watchRecord),
		sessionRefs:       make(map[string]int),
		aggregates:        make(map[string]watchAggregate),
		now:               time.Now,
		expireInterval:    time.Second,
	}
	return wm
}

func (wm *WatchManager) SetExpireCallback(fn func(WatchLease)) {
	wm.mu.Lock()
	wm.onExpire = fn
	wm.mu.Unlock()
}
func (wm *WatchManager) Close() {
	wm.mu.Lock()
	wm.closed = true
	stop, done, running := wm.stop, wm.done, wm.running
	wm.mu.Unlock()
	if running {
		close(stop)
		<-done
	}
}

func (wm *WatchManager) expiryLoop(stop <-chan struct{}, done chan<- struct{}) {
	t := time.NewTicker(wm.expireInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			wm.mu.Lock()
			expired := wm.expireLocked(wm.now())
			fn := wm.onExpire
			empty := len(wm.leases) == 0
			if empty {
				wm.running = false
			}
			wm.mu.Unlock()
			if fn != nil {
				for _, lease := range expired {
					fn(lease)
				}
			}
			if empty {
				close(done)
				return
			}
		case <-stop:
			wm.mu.Lock()
			wm.running = false
			wm.mu.Unlock()
			close(done)
			return
		}
	}
}

func (wm *WatchManager) OpenLease(watchID, connectionID string, epoch uint64, sessionID string) (WatchLease, bool, error) {
	if watchID == "" || connectionID == "" || sessionID == "" {
		return WatchLease{}, false, fmt.Errorf("watch_id, connection_id, and session_id are required")
	}
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.expireLocked(wm.now())
	if wm.closed {
		return WatchLease{}, false, fmt.Errorf("watch manager closed")
	}
	if old, ok := wm.leases[watchID]; ok {
		if old.ConnectionID != connectionID || old.Epoch != epoch || old.SessionID != sessionID {
			return WatchLease{}, false, fmt.Errorf("watch_id belongs to another connection epoch")
		}
		old.ExpiresAt = wm.now().Add(WatchLeaseDuration)
		wm.leases[watchID] = old
		return old.WatchLease, false, nil
	}
	lease := WatchLease{WatchID: watchID, ConnectionID: connectionID, Epoch: epoch, SessionID: sessionID, ExpiresAt: wm.now().Add(WatchLeaseDuration)}
	wm.leases[watchID] = watchRecord{WatchLease: lease}
	if wm.sessionRefs[sessionID] == 0 {
		upstreamID, err := randomWatchIdentity("hem-upstream")
		if err != nil {
			delete(wm.leases, watchID)
			return WatchLease{}, false, err
		}
		upstreamEpoch, err := randomWatchEpoch()
		if err != nil {
			delete(wm.leases, watchID)
			return WatchLease{}, false, err
		}
		wm.aggregates[sessionID] = watchAggregate{session: sessionID, upstream: WatchLease{
			WatchID: upstreamID, ConnectionID: "hem-" + upstreamID, Epoch: upstreamEpoch, SessionID: sessionID,
		}}
	}
	wm.sessionRefs[sessionID]++
	if !wm.running {
		wm.running = true
		wm.stop = make(chan struct{})
		wm.done = make(chan struct{})
		go wm.expiryLoop(wm.stop, wm.done)
	}
	return lease, wm.sessionRefs[sessionID] == 1, nil
}

func (wm *WatchManager) RenewLease(watchID, connectionID string, epoch uint64) (WatchLease, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.expireLocked(wm.now())
	lease, ok := wm.leases[watchID]
	if !ok || lease.ConnectionID != connectionID || lease.Epoch != epoch {
		return WatchLease{}, fmt.Errorf("stale or unknown watch lease")
	}
	lease.ExpiresAt = wm.now().Add(WatchLeaseDuration)
	wm.leases[watchID] = lease
	return lease.WatchLease, nil
}

func (wm *WatchManager) CloseLease(watchID, connectionID string, epoch uint64) (string, bool, WatchLease) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	lease, ok := wm.leases[watchID]
	if !ok || lease.ConnectionID != connectionID || lease.Epoch != epoch {
		return "", false, WatchLease{}
	}
	delete(wm.leases, watchID)
	wm.sessionRefs[lease.SessionID]--
	if wm.sessionRefs[lease.SessionID] <= 0 {
		delete(wm.sessionRefs, lease.SessionID)
		if aggregate, ok := wm.aggregates[lease.SessionID]; ok {
			delete(wm.aggregates, lease.SessionID)
			return lease.SessionID, true, aggregate.upstream
		}
		delete(wm.aggregates, lease.SessionID)
		return lease.SessionID, true, WatchLease{}
	}
	return lease.SessionID, false, WatchLease{}
}

func (wm *WatchManager) CloseConnection(connectionID string, epoch uint64) []WatchLease {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	var upstreams []WatchLease
	for id, lease := range wm.leases {
		if lease.ConnectionID == connectionID && lease.Epoch == epoch {
			delete(wm.leases, id)
			wm.sessionRefs[lease.SessionID]--
			if wm.sessionRefs[lease.SessionID] <= 0 {
				delete(wm.sessionRefs, lease.SessionID)
				if aggregate, ok := wm.aggregates[lease.SessionID]; ok {
					delete(wm.aggregates, lease.SessionID)
					upstreams = append(upstreams, aggregate.upstream)
				}
			}
		}
	}
	return upstreams
}

func (wm *WatchManager) Expire() []WatchLease {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	return wm.expireLocked(wm.now())
}

func (wm *WatchManager) expireLocked(now time.Time) []WatchLease {
	var expired []WatchLease
	for id, lease := range wm.leases {
		if now.Before(lease.ExpiresAt) {
			continue
		}
		delete(wm.leases, id)
		wm.sessionRefs[lease.SessionID]--
		if wm.sessionRefs[lease.SessionID] <= 0 {
			delete(wm.sessionRefs, lease.SessionID)
			if aggregate, ok := wm.aggregates[lease.SessionID]; ok {
				delete(wm.aggregates, lease.SessionID)
				expired = append(expired, aggregate.upstream)
			}
		}
	}
	return expired
}

func (wm *WatchManager) RefCount(sessionID string) int {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.expireLocked(wm.now())
	return wm.sessionRefs[sessionID]
}

// AddWatcher registers a child session to be watched by a parent session.
func (wm *WatchManager) AddWatcher(parentSessionID, childSessionID string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.watchers[parentSessionID] = append(wm.watchers[parentSessionID], childSessionID)
}

func (wm *WatchManager) GetWatchers(parentSessionID string) []string {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	return append([]string(nil), wm.watchers[parentSessionID]...)
}

func (wm *WatchManager) RemoveWatcher(parentSessionID, childSessionID string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	children := wm.watchers[parentSessionID]
	for i, c := range children {
		if c == childSessionID {
			wm.watchers[parentSessionID] = append(children[:i], children[i+1:]...)
			break
		}
	}
	if len(wm.watchers[parentSessionID]) == 0 {
		delete(wm.watchers, parentSessionID)
	}
}

func (wm *WatchManager) SetLastState(sessionID, state string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.lastSessionStates[sessionID] = state
}

func (wm *WatchManager) GetLastState(sessionID string) (string, bool) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	state, ok := wm.lastSessionStates[sessionID]
	return state, ok
}

func (wm *WatchManager) DeleteState(sessionID string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	delete(wm.lastSessionStates, sessionID)
}
