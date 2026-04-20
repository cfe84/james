package commands

import (
	"sync"
	"time"

	"james/hem/pkg/store"
	"james/hem/pkg/transport"
)

// mpCooldownDuration is how long to skip querying a moneypenny after it fails.
const mpCooldownDuration = 30 * time.Second

// ClientManager manages transport client lifecycle and circuit breaking.
type ClientManager struct {
	clients    map[string]*transport.Client // cached per moneypenny name
	cooldowns  map[string]time.Time         // mpName → earliest time to retry after failure
	mu         sync.Mutex
	mi6KeyPath string
}

// NewClientManager creates a new ClientManager.
func NewClientManager(mi6KeyPath string) *ClientManager {
	return &ClientManager{
		clients:    make(map[string]*transport.Client),
		cooldowns:  make(map[string]time.Time),
		mi6KeyPath: mi6KeyPath,
	}
}

// GetClient returns a cached or newly created client for the given moneypenny.
// Returns nil if the transport type is unsupported.
func (cm *ClientManager) GetClient(mp *store.Moneypenny) *transport.Client {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if c, ok := cm.clients[mp.Name]; ok {
		return c
	}

	var c *transport.Client
	switch mp.TransportType {
	case store.TransportFIFO:
		c = transport.NewFIFOClient(mp.FIFOIn, mp.FIFOOut)
	case store.TransportMI6:
		c = transport.NewMI6Client(mp.MI6Addr, cm.mi6KeyPath)
	default:
		return nil
	}
	cm.clients[mp.Name] = c
	return c
}

// SetCooldown marks a moneypenny as failed and sets a cooldown period.
func (cm *ClientManager) SetCooldown(mpName string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cooldowns[mpName] = time.Now().Add(mpCooldownDuration)
}

// IsInCooldown returns true if the moneypenny is in cooldown period.
func (cm *ClientManager) IsInCooldown(mpName string) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cooldownUntil, ok := cm.cooldowns[mpName]; ok {
		return time.Now().Before(cooldownUntil)
	}
	return false
}

// ClearCooldown clears the cooldown for a moneypenny.
func (cm *ClientManager) ClearCooldown(mpName string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.cooldowns, mpName)
}

// GetCooldownUntil returns the cooldown expiry time for a moneypenny, or zero if not in cooldown.
func (cm *ClientManager) GetCooldownUntil(mpName string) time.Time {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cooldownUntil, ok := cm.cooldowns[mpName]; ok && time.Now().Before(cooldownUntil) {
		return cooldownUntil
	}
	return time.Time{}
}

// MI6KeyPath returns the MI6 key path.
func (cm *ClientManager) MI6KeyPath() string {
	return cm.mi6KeyPath
}
