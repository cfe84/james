package channel

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"sync"
)

type pluginProvider struct {
	name string
	cmd  string
	mu   sync.Mutex
	cli  *pluginClient
}

func newPluginProvider(name, cmd string) *pluginProvider {
	return &pluginProvider{name: name, cmd: cmd}
}

func (p *pluginProvider) client() *pluginClient {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cli != nil {
		return p.cli
	}
	c := newPluginClient(p.cmd)
	c.start = func() (io.ReadWriteCloser, error) {
		cmd := exec.Command(p.cmd)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &pluginProcess{ReadWriteCloser: structReadWriteCloser{Reader: stdout, Writer: stdin, Closer: func() error {
			_ = stdin.Close()
			return cmd.Process.Kill()
		}}, cmd: cmd}, nil
	}
	p.cli = c
	return c
}

func (p *pluginProvider) Name() string { return p.name }
func (p *pluginProvider) Caps() Caps   { return Caps{Search: true, ProvideID: true} }
func (p *pluginProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cli != nil {
		p.cli.close()
	}
}

func (p *pluginProvider) call(method string, params interface{}, out interface{}) error {
	return p.callContext(context.Background(), method, params, out)
}

func (p *pluginProvider) callContext(ctx context.Context, method string, params interface{}, out interface{}) error {
	raw, err := p.client().request(ctx, method, params)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (p *pluginProvider) Search(ctx context.Context, query string) ([]Target, error) {
	var out []Target
	err := p.callContext(ctx, "search", map[string]string{"query": query}, &out)
	return out, err
}
func (p *pluginProvider) Resolve(ctx context.Context, targetID string) (Target, error) {
	var out Target
	err := p.callContext(ctx, "resolve", map[string]string{"target_id": targetID}, &out)
	return out, err
}
func (p *pluginProvider) ListMessages(ctx context.Context, targetID, sinceTS string) ([]Message, error) {
	var out []Message
	err := p.callContext(ctx, "list_messages", map[string]string{"target_id": targetID, "since_ts": sinceTS}, &out)
	return out, err
}
func (p *pluginProvider) Send(ctx context.Context, targetID, senderName, content string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := p.callContext(ctx, "send", map[string]string{"target_id": targetID, "sender_name": senderName, "content": content}, &out)
	return out.ID, err
}
func (p *pluginProvider) LatestCursor(ctx context.Context, targetID string) (string, string, error) {
	var out struct {
		ID string `json:"id"`
		TS string `json:"timestamp"`
	}
	err := p.callContext(ctx, "latest_cursor", map[string]string{"target_id": targetID}, &out)
	return out.ID, out.TS, err
}
func (p *pluginProvider) Self(ctx context.Context) (string, string, error) {
	var out struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	err := p.callContext(ctx, "self", nil, &out)
	return out.ID, out.Name, err
}

type structReadWriteCloser struct {
	io.Reader
	io.Writer
	Closer func() error
}

func (s structReadWriteCloser) Close() error { return s.Closer() }

type pluginProcess struct {
	io.ReadWriteCloser
	cmd *exec.Cmd
}

var _ Provider = (*pluginProvider)(nil)
