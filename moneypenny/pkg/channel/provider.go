package channel

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Message is a normalized inbound/outbound message on a channel target.
type Message struct {
	ID         string
	Timestamp  string // RFC3339 (provider-native ordering key)
	SenderID   string
	SenderName string
	Text       string
}

// Target identifies a concrete conversation within a provider (a Teams chat, an
// email thread, ...). Label/Detail are for display in pickers.
type Target struct {
	ID     string
	Label  string
	Detail string
}

// Caps describes which target-selection methods a provider supports.
type Caps struct {
	Search    bool // Search(query) returns candidate targets
	ProvideID bool // a raw target id may be supplied directly
}

// Provider is a communication backend (Teams today). Implementations wrap an
// mcpClient and translate to/from provider-specific tool calls.
type Provider interface {
	// Name is the stable provider key (e.g. "teams").
	Name() string
	// Caps reports supported target-selection methods.
	Caps() Caps
	// Search returns candidate targets matching a free-text query. Only valid
	// when Caps().Search is true.
	Search(ctx context.Context, query string) ([]Target, error)
	// Resolve validates/normalizes a directly-provided target id and returns a
	// display label. Only valid when Caps().ProvideID is true.
	Resolve(ctx context.Context, targetID string) (Target, error)
	// ListMessages returns messages on the target strictly newer than sinceTS
	// (RFC3339). When sinceTS is empty, providers should return only the most
	// recent messages so a freshly-bound channel doesn't replay history.
	ListMessages(ctx context.Context, targetID, sinceTS string) ([]Message, error)
	// Send posts content to the target and returns the created message id.
	Send(ctx context.Context, targetID, content string) (string, error)
	// LatestCursor returns the id and timestamp (RFC3339) of the most recent
	// message on the target, used to initialize a channel's poll cursor at bind
	// time so pre-existing history is not replayed. Empty strings mean the
	// target has no messages yet.
	LatestCursor(ctx context.Context, targetID string) (id, ts string, err error)
	// Self returns the id (and best-effort display name) of the signed-in
	// account driving the provider, used to gate inbound messages to the owner
	// when a channel disallows messages from others. An empty id with nil error
	// means the provider cannot determine identity.
	Self(ctx context.Context) (id, name string, err error)
	// Close releases provider resources (subprocess, etc).
	Close()
}

// Registry constructs and caches provider instances by name. Providers share a
// single MCP subprocess per moneypenny process (one `agency mcp teams` child).
type Registry struct {
	mu        sync.Mutex
	command   string // base command, e.g. "agency"
	providers map[string]Provider
}

// NewRegistry returns a registry. command is the executable used to launch
// provider MCP servers (default "agency"); each provider appends its own args.
func NewRegistry(command string) *Registry {
	if command == "" {
		command = "agency"
	}
	return &Registry{command: command, providers: map[string]Provider{}}
}

// Get returns (constructing if needed) the provider for name.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.providers[name]; ok {
		return p, nil
	}
	var p Provider
	switch name {
	case "teams":
		p = newTeamsProvider(newMCPClient(r.command, "mcp", "teams"))
	default:
		return nil, fmt.Errorf("unknown channel provider %q", name)
	}
	r.providers[name] = p
	return p, nil
}

// ProviderMeta describes an available provider for listing to clients.
type ProviderMeta struct {
	Name string `json:"name"`
	Caps Caps   `json:"caps"`
}

// List returns metadata for all known providers (whether or not instantiated).
func (r *Registry) List() []ProviderMeta {
	metas := []ProviderMeta{
		{Name: "teams", Caps: Caps{Search: true, ProvideID: true}},
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Name < metas[j].Name })
	return metas
}

// Close tears down all instantiated providers.
func (r *Registry) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.providers {
		p.Close()
	}
	r.providers = map[string]Provider{}
}
