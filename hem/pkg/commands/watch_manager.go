package commands

import (
	"sync"
)

// WatchManager manages the watch/polling mechanism for session state tracking.
type WatchManager struct {
	watchers          map[string][]string // parentSessionID → []childSessionIDs being watched
	lastSessionStates map[string]string   // sessionID → last known mpStatus ("working", "ready", etc.)
	mu                sync.Mutex
}

// NewWatchManager creates a new WatchManager.
func NewWatchManager() *WatchManager {
	return &WatchManager{
		watchers:          make(map[string][]string),
		lastSessionStates: make(map[string]string),
	}
}

// AddWatcher registers a child session to be watched by a parent session.
func (wm *WatchManager) AddWatcher(parentSessionID, childSessionID string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.watchers[parentSessionID] = append(wm.watchers[parentSessionID], childSessionID)
}

// GetWatchers returns the list of child sessions being watched by a parent.
func (wm *WatchManager) GetWatchers(parentSessionID string) []string {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	return wm.watchers[parentSessionID]
}

// RemoveWatcher removes a child session from the watch list of a parent.
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

// SetLastState records the last known state of a session.
func (wm *WatchManager) SetLastState(sessionID, state string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.lastSessionStates[sessionID] = state
}

// GetLastState returns the last known state of a session.
func (wm *WatchManager) GetLastState(sessionID string) (string, bool) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	state, ok := wm.lastSessionStates[sessionID]
	return state, ok
}

// DeleteState removes the state tracking for a session.
func (wm *WatchManager) DeleteState(sessionID string) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	delete(wm.lastSessionStates, sessionID)
}
