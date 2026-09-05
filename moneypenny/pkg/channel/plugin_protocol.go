package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

const ChannelPluginProtocolVersion = "1"

type pluginMessage struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *pluginError    `json:"error,omitempty"`
	Event   string          `json:"event,omitempty"`
	Payload interface{}     `json:"payload,omitempty"`
	Version string          `json:"version,omitempty"`
}

type pluginError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *pluginError) Error() string {
	return fmt.Sprintf("channel plugin %s: %s", e.Code, e.Message)
}

type pluginClient struct {
	cmdName string
	start   func() (io.ReadWriteCloser, error)
	conn    io.ReadWriteCloser

	mu          sync.Mutex
	writeMu     sync.Mutex
	pending     map[string]chan pluginMessage
	events      chan pluginMessage
	initialized bool
}

func newPluginClient(cmd string) *pluginClient {
	return &pluginClient{
		cmdName: cmd,
		pending: make(map[string]chan pluginMessage),
		events:  make(chan pluginMessage, 32),
	}
}

func (c *pluginClient) serve(r io.Reader, w io.WriteCloser) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var msg pluginMessage
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		if msg.Type == "response" {
			c.mu.Lock()
			ch := c.pending[msg.ID]
			delete(c.pending, msg.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
				close(ch)
			}
			continue
		}
		if msg.Type == "event" {
			select {
			case c.events <- msg:
			default:
			}
		}
	}
	_ = w.Close()
	c.mu.Lock()
	c.conn = nil
	for id, ch := range c.pending {
		ch <- pluginMessage{Type: "response", ID: id, Error: &pluginError{Code: "closed", Message: "plugin exited"}}
		close(ch)
	}
	c.pending = make(map[string]chan pluginMessage)
	c.mu.Unlock()
}

func (c *pluginClient) request(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	if c.conn == nil {
		if c.start == nil {
			c.mu.Unlock()
			return nil, fmt.Errorf("channel plugin %q is not configured", c.cmdName)
		}
		rw, err := c.start()
		if err != nil {
			c.mu.Unlock()
			return nil, fmt.Errorf("start channel plugin: %w", err)
		}
		c.conn = rw
		go c.serve(rw, rw)
	}
	rw := c.conn
	needsInit := !c.initialized && method != "initialize"
	if needsInit {
		c.initialized = true
	}
	c.mu.Unlock()

	if needsInit {
		if _, err := c.request(ctx, "initialize", map[string]interface{}{
			"protocol_version": ChannelPluginProtocolVersion,
			"client":           "moneypenny",
		}); err != nil {
			c.mu.Lock()
			c.initialized = false
			c.mu.Unlock()
			return nil, err
		}
	}

	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("%d", nextPluginRequestID())
	ch := make(chan pluginMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	msg, err := json.Marshal(pluginMessage{Type: "request", ID: id, Method: method, Params: rawParams, Version: ChannelPluginProtocolVersion})
	if err != nil {
		return nil, err
	}
	c.writeMu.Lock()
	_, err = rw.Write(append(msg, '\n'))
	c.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write channel plugin: %w", err)
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return json.Marshal(resp.Result)
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *pluginClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.initialized = false
}

var pluginRequestCounter struct {
	sync.Mutex
	n uint64
}

func nextPluginRequestID() uint64 {
	pluginRequestCounter.Lock()
	defer pluginRequestCounter.Unlock()
	pluginRequestCounter.n++
	return pluginRequestCounter.n
}
