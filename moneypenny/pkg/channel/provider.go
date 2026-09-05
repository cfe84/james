package channel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
	// Send posts a message to the target and returns the created message id.
	// senderName, when non-empty, identifies the agent that produced the message
	// so the provider can render an attribution prefix in its native format
	// (e.g. Teams renders "[🤖 name]" italicized above the body). content is the
	// raw message body.
	Send(ctx context.Context, targetID, senderName, content string) (string, error)
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
	pluginCmd string
	providers map[string]Provider
}

// NewRegistry returns a registry. command is the executable used to launch
// provider MCP servers (default "agency"); each provider appends its own args.
func NewRegistry(command string) *Registry {
	if command == "" {
		command = "agency"
	}
	return &Registry{command: resolveCommand(command), providers: map[string]Provider{}}
}

// NewPluginRegistry returns a registry backed by supervised provider plugins.
// The plugin executable is started lazily on the first provider operation.
func NewPluginRegistry(command string) *Registry {
	return &Registry{pluginCmd: command, providers: map[string]Provider{}}
}

// resolveCommand turns a bare command name into an absolute path so provider
// subprocesses can be launched even when the process PATH is minimal (e.g. under
// launchd/systemd). If command already contains a path separator it is returned
// unchanged. Otherwise we try PATH, then a set of well-known install locations
// (notably the versioned agency install under ~/.config/agency). If nothing is
// found the original name is returned so exec still produces a clear error.
func resolveCommand(command string) string {
	if strings.ContainsRune(command, os.PathSeparator) {
		return command
	}
	if p, err := exec.LookPath(command); err == nil {
		return p
	}
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".config", command, "CurrentVersion", command),
			filepath.Join(home, ".local", "bin", command),
			filepath.Join(home, "bin", command),
		)
	}
	candidates = append(candidates,
		filepath.Join("/opt/homebrew/bin", command),
		filepath.Join("/usr/local/bin", command),
	)
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return command
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
		if r.pluginCmd != "" {
			p = newPluginProvider(name, r.pluginCmd)
		} else {
			p = newTeamsProvider(newMCPClient(r.command, "mcp", "teams"))
		}
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
