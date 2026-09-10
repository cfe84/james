package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	MaxRequestBytes  = 1 << 20
	MaxResponseBytes = 4 << 20
)

const Help = `gadgets — session-scoped tools provided by Moneypenny

Usage:
  gadgets memory get [path] [--offset N] [--limit N]
  gadgets memory list [path]
  gadgets memory search query
  gadgets memory set [path] [--body text]       (otherwise reads stdin)
  gadgets memory batch                       (JSON [{path,body}, ...] on stdin)
  gadgets memory delete path [--recursive]
  gadgets memory revisions [path]
  gadgets agents list
  gadgets agents message id [--body text]     (otherwise reads stdin)
  gadgets traits list
  gadgets traits get ID                      (ID or exact name)
  gadgets traits edit ID                     (complete body on stdin; empty clears; own traits only)
  gadgets subagents list
  gadgets subagents create [--name name] [--agent agent] [--model model] [--path path]
                                             (prompt on stdin)
  gadgets subagents message id [--body text]   (otherwise reads stdin)
  gadgets schedule list
  gadgets schedule create (--cron expr | --at timestamp) --prompt text
  gadgets schedule delete id
  gadgets notify [text]                      (otherwise reads stdin)
  gadgets help
  gadgets version

Flags may follow positional arguments; -- ends flag parsing.
Omitted memory paths refer to the root. Batch updates are atomic on the daemon.
Traits require opt-in permission (default: false). Edits replace shared trait
bodies for future use by all agents, not already-injected session prompts. Agents
may edit only traits assigned to their own session.
No trait create/delete, assignment, rename, or default-setting commands exist.
JAMES_GADGETS_URL and JAMES_GADGETS_TOKEN are supplied by the daemon.
Success envelopes are written as JSON to stdout, error envelopes to stderr.
`

type Request struct {
	Method string         `json:"method"`
	Data   map[string]any `json:"data"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type response struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *APIError       `json:"error,omitempty"`
}

func fail(w io.Writer, code, message string) int {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": false, "error": APIError{Code: code, Message: message},
	})
	return 1
}

// Run is independent of process globals so callers can test parsing and I/O.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, version string) int {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		_, _ = io.WriteString(stdout, Help)
		return 0
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		_ = json.NewEncoder(stdout).Encode(map[string]string{"version": version})
		return 0
	}
	if (len(args) == 2 || len(args) == 3) && (args[len(args)-1] == "--help" || args[len(args)-1] == "-h") {
		groupHelp := len(args) == 2 && (args[0] == "memory" || args[0] == "agents" || args[0] == "traits" || args[0] == "subagents" || args[0] == "schedule" || args[0] == "notify")
		_, rest, err := command(args)
		if groupHelp || (len(args) == 3 && err == nil && len(rest) == 1) {
			_, _ = io.WriteString(stdout, Help)
			return 0
		}
	}
	req, err := Parse(args, stdin)
	if err != nil {
		return fail(stderr, "usage", err.Error())
	}
	endpoint, err := localURL(getenv("JAMES_GADGETS_URL"))
	if err != nil {
		return fail(stderr, "configuration", err.Error())
	}
	token := getenv("JAMES_GADGETS_TOKEN")
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return fail(stderr, "configuration", "JAMES_GADGETS_TOKEN must contain a bearer token")
	}
	payload, err := json.Marshal(req)
	if err != nil || len(payload) > MaxRequestBytes {
		return fail(stderr, "request_too_large", "JSON request exceeds 1 MiB")
	}
	httpReq, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return fail(stderr, "configuration", "invalid daemon endpoint")
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxResponseHeaderBytes: 32 << 10,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Do(httpReq)
	if err != nil {
		return fail(stderr, "transport", "cannot reach local gadget daemon")
	}
	defer res.Body.Close()
	body, err := readBounded(res.Body, MaxResponseBytes)
	if err != nil {
		return fail(stderr, "protocol", "cannot read daemon response (maximum 4 MiB)")
	}
	var envelope response
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Success == nil {
		return fail(stderr, "protocol", "daemon returned an invalid response envelope")
	}
	if !*envelope.Success {
		if envelope.Error == nil || envelope.Error.Code == "" || envelope.Error.Message == "" {
			return fail(stderr, "protocol", "daemon returned a failure without an error")
		}
		if err := json.NewEncoder(stderr).Encode(envelope); err != nil {
			return 1
		}
		return 1
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || envelope.Error != nil {
		return fail(stderr, "protocol", fmt.Sprintf("inconsistent daemon response (HTTP %d)", res.StatusCode))
	}
	if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
		return fail(stderr, "output", "cannot write response")
	}
	return 0
}

func localURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return nil, errors.New("JAMES_GADGETS_URL must be an HTTP loopback endpoint without credentials, query, or fragment")
	}
	host := u.Hostname()
	if host == "localhost" {
		// Pin localhost rather than depending on DNS or hosts-file resolution.
		host = "127.0.0.1"
		if port := u.Port(); port != "" {
			u.Host = net.JoinHostPort(host, port)
		} else {
			u.Host = host
		}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" {
		return nil, errors.New("JAMES_GADGETS_URL must use localhost or a literal loopback address")
	}
	return u, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("input exceeds size limit")
	}
	return b, nil
}
