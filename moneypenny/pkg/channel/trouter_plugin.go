package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type trouterConfig struct {
	DryRun      bool   `json:"dry_run"`
	AuthCommand string `json:"auth_command,omitempty"`
}

type trouterPlugin struct {
	mu        sync.Mutex
	config    trouterConfig
	authState string
	account   string
	token     string
	expiresAt string
	provider  *nativeTeams
}

func newTrouterPlugin(dryRun bool, authCommand string) *trouterPlugin {
	return &trouterPlugin{
		config:    trouterConfig{DryRun: dryRun, AuthCommand: authCommand},
		authState: "not_started",
	}
}

func (p *trouterPlugin) initialize(cfg trouterConfig) {
	p.close()
	p.mu.Lock()
	defer p.mu.Unlock()
	if cfg.AuthCommand != "" {
		p.config.AuthCommand = cfg.AuthCommand
	}
	p.config.DryRun = cfg.DryRun
}

func (p *trouterPlugin) authenticate(ctx context.Context) error {
	p.close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	p.mu.Lock()
	cfg := p.config
	p.mu.Unlock()
	if cfg.DryRun {
		p.mu.Lock()
		p.authState = "dry_run"
		p.account = "dry-run"
		p.mu.Unlock()
		return nil
	}
	var token, account, expiresAt string
	if cfg.AuthCommand != "" {
		result, err := runAuthCommand(ctx, cfg.AuthCommand)
		if err != nil {
			return err
		}
		token, account, expiresAt = result.Token, result.Account, result.ExpiresAt
	} else if runtime.GOOS == "darwin" {
		result, err := authenticateWithAzureCLI(ctx)
		if err != nil {
			return err
		}
		token, account, expiresAt = result.Token, result.Account, result.ExpiresAt
	} else if runtime.GOOS == "windows" {
		return fmt.Errorf("set JAMES_TEAMS_AUTH_COMMAND to a Windows delegated-auth helper")
	} else {
		return fmt.Errorf("Teams delegated authentication is currently supported on macOS and Windows")
	}
	if strings.TrimSpace(account) == "" {
		return fmt.Errorf("delegated authentication returned no account")
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("delegated authentication returned no access token")
	}
	p.mu.Lock()
	p.authState = "authenticated"
	p.account = account
	p.token = token
	p.expiresAt = expiresAt
	p.mu.Unlock()
	return nil
}

type authResult struct {
	Token     string
	Account   string
	ExpiresAt string
}

func runAuthCommand(ctx context.Context, command string) (authResult, error) {
	cmd := exec.CommandContext(ctx, command)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return authResult{}, ctx.Err()
		}
		return authResult{}, fmt.Errorf("delegated auth command failed")
	}
	var result struct {
		Account   string `json:"account"`
		Token     string `json:"access_token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return authResult{}, fmt.Errorf("delegated auth command returned invalid JSON: %w", err)
	}
	return authResult{Token: result.Token, Account: result.Account, ExpiresAt: result.ExpiresAt}, nil
}

func authenticateWithAzureCLI(ctx context.Context) (authResult, error) {
	tokenCmd := exec.CommandContext(ctx, "az", "account", "get-access-token",
		"--resource", teamsOAuthResource, "--output", "json")
	tokenCmd.WaitDelay = time.Second
	out, err := tokenCmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return authResult{}, ctx.Err()
		}
		return authResult{}, fmt.Errorf("Azure CLI Teams token unavailable (run az login first)")
	}
	var token struct {
		AccessToken string `json:"accessToken"`
		ExpiresOn   string `json:"expiresOn"`
	}
	if err := json.Unmarshal(out, &token); err != nil || token.AccessToken == "" {
		return authResult{}, fmt.Errorf("Azure CLI returned no Teams access token")
	}
	accountCmd := exec.CommandContext(ctx, "az", "account", "show", "--output", "json")
	accountCmd.WaitDelay = time.Second
	accountOut, err := accountCmd.Output()
	if err != nil {
		return authResult{}, fmt.Errorf("Azure CLI account lookup: %w", err)
	}
	var account struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal(accountOut, &account); err != nil || strings.TrimSpace(account.User.Name) == "" {
		return authResult{}, fmt.Errorf("Azure CLI account lookup returned no user")
	}
	return authResult{Token: token.AccessToken, Account: account.User.Name, ExpiresAt: token.ExpiresOn}, nil
}

func (p *trouterPlugin) status() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := "disconnected"
	if p.provider != nil && time.Now().Before(p.provider.expiresAt) {
		state = "connected"
	}
	return map[string]string{"auth": p.authState, "account": p.account, "transport": "polling", "connection": state}
}

func (p *trouterPlugin) close() {
	p.provider = nil
	p.mu.Lock()
	p.token, p.expiresAt, p.account, p.authState = "", "", "", "not_started"
	p.mu.Unlock()
}

func (p *trouterPlugin) connect(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if p.provider != nil {
		if time.Now().Before(p.provider.expiresAt) {
			return nil
		}
		if err := p.authenticate(ctx); err != nil {
			return err
		}
	}
	p.mu.Lock()
	auth := authResult{Token: p.token, Account: p.account, ExpiresAt: p.expiresAt}
	state := p.authState
	p.mu.Unlock()
	if state != "authenticated" {
		return fmt.Errorf("authenticate before connect")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	provider, err := newNativeTeams(auth, client)
	if err != nil {
		p.close()
		return err
	}
	p.provider = provider
	return nil
}

func runTrouterPlugin(ctx context.Context, in io.Reader, out io.Writer, dryRun bool, authCommand string) error {
	p := newTrouterPlugin(dryRun, authCommand)
	defer p.close()
	enc := json.NewEncoder(out)
	write := func(msg pluginMessage) error {
		return enc.Encode(msg)
	}
	emit := func(event string, payload interface{}) error {
		return write(pluginMessage{Type: "event", Event: event, Payload: payload, Version: ChannelPluginProtocolVersion})
	}
	if err := emit("status", map[string]string{"state": "starting", "transport": "polling"}); err != nil {
		return err
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var req pluginMessage
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		if req.Type != "request" {
			continue
		}
		resp := pluginMessage{Type: "response", ID: req.ID, Version: ChannelPluginProtocolVersion}
		if req.Version != "" && req.Version != ChannelPluginProtocolVersion {
			resp.Error = &pluginError{Code: "unsupported_version", Message: req.Version}
		} else {
			result, err := p.handle(ctx, req.Method, req.Params)
			if err != nil {
				resp.Error = &pluginError{Code: "trouter_error", Message: err.Error()}
				_ = emit("status", map[string]string{"state": "degraded", "error": err.Error()})
			} else {
				resp.Result = result
				if req.Method == "authenticate" {
					_ = emit("status", map[string]string{"state": "authenticated", "auth": p.status()["auth"]})
				} else if req.Method == "connect" {
					_ = emit("status", result)
				}
			}
		}
		if err := write(resp); err != nil {
			return err
		}
		if req.Method == "shutdown" {
			_ = emit("status", map[string]string{"state": "stopped"})
			return nil
		}
	}
	return scanner.Err()
}

func (p *trouterPlugin) handle(ctx context.Context, method string, raw json.RawMessage) (interface{}, error) {
	switch method {
	case "initialize":
		return map[string]interface{}{"protocol_version": ChannelPluginProtocolVersion, "name": "teams-trouter",
			"caps": map[string]bool{"push": false, "send": true, "Search": true, "ProvideID": true}, "platform": runtime.GOOS}, nil
	case "configure":
		var cfg trouterConfig
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return nil, fmt.Errorf("decode configure parameters: %w", err)
			}
		}
		p.initialize(cfg)
		return p.status(), nil
	case "authenticate":
		if err := p.authenticate(ctx); err != nil {
			return nil, err
		}
		return p.status(), nil
	case "status":
		return p.status(), nil
	case "connect":
		p.mu.Lock()
		auth := p.authState
		p.mu.Unlock()
		if auth != "authenticated" && auth != "dry_run" {
			return nil, fmt.Errorf("authenticate before connect")
		}
		if auth == "dry_run" {
			return map[string]string{"state": "dry_run_ready"}, nil
		}
		if err := p.connect(ctx); err != nil {
			return nil, err
		}
		return map[string]string{"state": "connected"}, nil
	case "shutdown":
		p.close()
		return map[string]string{"state": "stopping"}, nil
	case "search", "resolve", "list_messages", "latest_cursor", "self", "send":
		p.mu.Lock()
		state := p.authState
		p.mu.Unlock()
		if state == "dry_run" || p.config.DryRun {
			return nil, fmt.Errorf("Teams message operations are unavailable in dry-run mode")
		}
		if state != "authenticated" {
			if err := p.authenticate(ctx); err != nil {
				return nil, err
			}
		}
		if err := p.connect(ctx); err != nil {
			return nil, err
		}
		result, err := runPluginMethod(ctx, p.provider, method, raw)
		var httpErr teamsHTTPError
		if errors.As(err, &httpErr) && (httpErr == http.StatusUnauthorized || httpErr == http.StatusForbidden) {
			p.close()
		}
		return result, err
	default:
		return nil, fmt.Errorf("unsupported Trouter method %q", method)
	}
}

func RunTrouterPluginFromEnv(ctx context.Context, in io.Reader, out io.Writer, dryRun bool) error {
	authCommand := os.Getenv("JAMES_TEAMS_AUTH_COMMAND")
	return runTrouterPlugin(ctx, in, out, dryRun, authCommand)
}
