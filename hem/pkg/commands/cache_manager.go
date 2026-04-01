package commands

import (
	"sync"
	"time"
)

// CacheManager manages moneypenny session caching.
// Dashboard/ListSessions return instantly from this cache while a background refresh runs.
type CacheManager struct {
	mpCache      map[string]map[string]mpSessionInfo // mpName → sessionID → info
	cacheTime    time.Time                           // when the cache was last fully refreshed
	refreshing   bool                                // true while a background refresh is in progress
	mu           sync.RWMutex
}

// NewCacheManager creates a new CacheManager.
func NewCacheManager() *CacheManager {
	return &CacheManager{
		mpCache: make(map[string]map[string]mpSessionInfo),
	}
}

// GetSnapshot returns a deep copy of the cached data.
func (cm *CacheManager) GetSnapshot() map[string]map[string]mpSessionInfo {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	snapshot := make(map[string]map[string]mpSessionInfo, len(cm.mpCache))
	for mpName, sessions := range cm.mpCache {
		m := make(map[string]mpSessionInfo, len(sessions))
		for k, v := range sessions {
			m[k] = v
		}
		snapshot[mpName] = m
	}
	return snapshot
}

// Update replaces the cache with new data.
func (cm *CacheManager) Update(data map[string]map[string]mpSessionInfo) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.mpCache = data
	cm.cacheTime = time.Now()
}

// UpdateMP updates the cache for a single moneypenny without replacing the rest.
func (cm *CacheManager) UpdateMP(mpName string, sessions map[string]mpSessionInfo) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.mpCache[mpName] = sessions
	cm.cacheTime = time.Now()
}

// GetCacheTime returns when the cache was last refreshed.
func (cm *CacheManager) GetCacheTime() time.Time {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.cacheTime
}

// IsRefreshing returns true if a background refresh is in progress.
func (cm *CacheManager) IsRefreshing() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.refreshing
}

// SetRefreshing sets the refreshing flag.
func (cm *CacheManager) SetRefreshing(refreshing bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.refreshing = refreshing
}
