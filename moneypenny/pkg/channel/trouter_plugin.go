package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
}

func newTrouterPlugin(dryRun bool, authCommand string) *trouterPlugin {
	return &trouterPlugin{
		config:    trouterConfig{DryRun: dryRun, AuthCommand: authCommand},
		authState: "not_started",
	}
}

func (p *trouterPlugin) initialize(cfg trouterConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cfg.AuthCommand != "" {
		p.config.AuthCommand = cfg.AuthCommand
	}
	p.config.DryRun = cfg.DryRun
}

func (p *trouterPlugin) authenticate(ctx context.Context) error {
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
	out, err := cmd.Output()
	if err != nil {
		return authResult{}, fmt.Errorf("delegated auth command: %w", err)
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
		"--resource", "https://ic3.teams.office.com", "--output", "json")
	out, err := tokenCmd.Output()
	if err != nil {
		return authResult{}, fmt.Errorf("Azure CLI Teams token: %w (run az login first)", err)
	}
	var token struct {
		AccessToken string `json:"accessToken"`
		ExpiresOn   string `json:"expiresOn"`
	}
	if err := json.Unmarshal(out, &token); err != nil || token.AccessToken == "" {
		return authResult{}, fmt.Errorf("Azure CLI returned no Teams access token")
	}
	accountCmd := exec.CommandContext(ctx, "az", "account", "show", "--output", "json")
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
	return map[string]string{"auth": p.authState, "account": p.account, "transport": "trouter"}
}

func runTrouterPlugin(ctx context.Context, in io.Reader, out io.Writer, dryRun bool, authCommand string) error {
	p := newTrouterPlugin(dryRun, authCommand)
	enc := json.NewEncoder(out)
	emit := func(event string, payload interface{}) error {
		return enc.Encode(pluginMessage{Type: "event", Event: event, Payload: payload, Version: ChannelPluginProtocolVersion})
	}
	if err := emit("status", map[string]string{"state": "starting", "transport": "trouter"}); err != nil {
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
		if err := enc.Encode(resp); err != nil {
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
		return map[string]interface{}{"protocol_version": ChannelPluginProtocolVersion, "name": "teams-trouter", "caps": map[string]bool{"push": true, "send": false}, "platform": runtime.GOOS}, nil
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
		return map[string]string{"state": "probe_ready", "registered_at": time.Now().UTC().Format(time.RFC3339)}, nil
	case "shutdown":
		return map[string]string{"state": "stopping"}, nil
	default:
		return nil, fmt.Errorf("unsupported Trouter probe method %q", method)
	}
}

func RunTrouterPluginFromEnv(ctx context.Context, in io.Reader, out io.Writer, dryRun bool) error {
	authCommand := os.Getenv("JAMES_TEAMS_AUTH_COMMAND")
	return runTrouterPlugin(ctx, in, out, dryRun, authCommand)
}
