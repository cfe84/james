package channel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const teamsOAuthResource = "https://ic3.teams.office.com"
const teamsChatBaseURL = "https://teams.cloud.microsoft/api/chatsvc/noam-canary"
const teamsTimestamp = "2006-01-02T15:04:05.000000000Z"
const teamsMaxResponse = 4 * 1024 * 1024

type teamsHTTPError int

func (e teamsHTTPError) Error() string {
	return fmt.Sprintf("Teams request returned HTTP %d", e)
}

type nativeTeams struct {
	http            *http.Client
	bearer, base    string
	selfID, account string
	expiresAt       time.Time
}

func newNativeTeams(auth authResult, client *http.Client) (*nativeTeams, error) {
	if strings.TrimSpace(auth.Token) == "" {
		return nil, fmt.Errorf("delegated authentication returned no access token")
	}
	p := &nativeTeams{http: client, bearer: auth.Token, account: auth.Account, base: teamsChatBaseURL,
		expiresAt: time.Now().Add(45 * time.Minute)}
	parts := strings.Split(auth.Token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("Teams access token has no user identity")
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("Teams access token has invalid identity claims")
	}
	var identity struct {
		OID string `json:"oid"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(claims, &identity) != nil || identity.OID == "" {
		return nil, fmt.Errorf("Teams access token has no delegated user identity")
	}
	p.selfID = "8:orgid:" + identity.OID
	if identity.Exp > 0 {
		if expiry := time.Unix(identity.Exp, 0).Add(-time.Minute); expiry.Before(p.expiresAt) {
			p.expiresAt = expiry
		}
	}
	if !time.Now().Before(p.expiresAt) {
		return nil, fmt.Errorf("Teams authentication token is expired or about to expire")
	}
	return p, nil
}

func (p *nativeTeams) request(ctx context.Context, method, address string, body, result any) (http.Header, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	for redirects := 0; redirects < 5; redirects++ {
		headers, nextBase, err := p.requestOnce(ctx, method, address, data, result)
		if err != nil || nextBase == "" {
			return headers, err
		}
		if !strings.HasPrefix(address, p.base+"/") {
			return nil, fmt.Errorf("Teams returned a redirect for an unexpected chat service path")
		}
		address = nextBase + strings.TrimPrefix(address, p.base)
		p.base = nextBase
	}
	return nil, fmt.Errorf("Teams chat service redirect limit exceeded")
}

func (p *nativeTeams) requestOnce(ctx context.Context, method, address string, data []byte, result any) (http.Header, string, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("invalid Teams request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.bearer)
	req.Header.Set("BehaviorOverride", "redirectAs404")
	resp, err := p.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "", fmt.Errorf("Teams request timed out (%s)", method)
		}
		return nil, "", fmt.Errorf("Teams request failed (%s)", method)
	}
	defer resp.Body.Close()
	if (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMovedPermanently ||
		resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusTemporaryRedirect ||
		resp.StatusCode == http.StatusPermanentRedirect) && resp.Header.Get("Location") != "" {
		next, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			return nil, "", fmt.Errorf("Teams returned an invalid chat service redirect")
		}
		next = req.URL.ResolveReference(next)
		parts := strings.Split(next.Path, "/")
		if next.Scheme != "https" || next.Host != "teams.cloud.microsoft" || next.User != nil ||
			next.Fragment != "" || len(parts) < 4 || parts[1] != "api" || parts[2] != "chatsvc" ||
			parts[3] == "" || parts[3] == "." || parts[3] == ".." {
			return nil, "", fmt.Errorf("Teams returned an unsafe chat service redirect")
		}
		return nil, "https://teams.cloud.microsoft/api/chatsvc/" + url.PathEscape(parts[3]), nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", teamsHTTPError(resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, teamsMaxResponse+1))
	if err != nil {
		return nil, "", fmt.Errorf("read Teams response: %w", err)
	}
	if len(data) > teamsMaxResponse {
		return nil, "", fmt.Errorf("Teams response exceeds size limit")
	}
	if result != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, result); err != nil {
			return nil, "", fmt.Errorf("Teams returned invalid JSON")
		}
	}
	if len(data) > 0 {
		var failure struct {
			ErrorCode json.RawMessage `json:"errorCode"`
		}
		if json.Unmarshal(data, &failure) == nil && len(failure.ErrorCode) > 0 &&
			string(failure.ErrorCode) != "0" && string(failure.ErrorCode) != "null" && string(failure.ErrorCode) != `""` {
			return nil, "", fmt.Errorf("Teams returned an application error")
		}
	}
	return resp.Header, "", nil
}

func (p *nativeTeams) Name() string { return "teams" }
func (p *nativeTeams) Caps() Caps   { return Caps{Search: true, ProvideID: true} }
func (p *nativeTeams) Close()       {}
func (p *nativeTeams) Self(context.Context) (string, string, error) {
	return p.selfID, p.account, nil
}

func (p *nativeTeams) conversationURL(target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", fmt.Errorf("Teams target_id is required")
	}
	return p.base + "/v1/users/ME/conversations/" + url.PathEscape(target), nil
}

func (p *nativeTeams) Search(ctx context.Context, query string) ([]Target, error) {
	var result struct {
		Conversations []struct {
			ID         string `json:"id"`
			Properties struct {
				Topic string `json:"topic"`
			} `json:"threadProperties"`
		} `json:"conversations"`
	}
	if _, err := p.request(ctx, http.MethodGet, p.base+"/v1/users/ME/conversations?pageSize=100", nil, &result); err != nil {
		return nil, err
	}
	if result.Conversations == nil {
		return nil, fmt.Errorf("Teams returned no conversation list")
	}
	targets := make([]Target, 0)
	for _, chat := range result.Conversations {
		label := chat.Properties.Topic
		if label == "" {
			label = chat.ID
		}
		if chat.ID != "" && strings.Contains(strings.ToLower(label), strings.ToLower(query)) {
			targets = append(targets, Target{ID: chat.ID, Label: label})
		}
	}
	return targets, nil
}

func (p *nativeTeams) Resolve(ctx context.Context, target string) (Target, error) {
	address, err := p.conversationURL(target)
	if err != nil {
		return Target{}, err
	}
	var result struct {
		ID         string `json:"id"`
		Properties struct {
			Topic string `json:"topic"`
		} `json:"threadProperties"`
	}
	if _, err := p.request(ctx, http.MethodGet, address, nil, &result); err != nil {
		return Target{}, err
	}
	if result.ID != target {
		return Target{}, fmt.Errorf("Teams returned a different conversation")
	}
	label := result.Properties.Topic
	if label == "" {
		label = target
	}
	return Target{ID: target, Label: label}, nil
}

type nativeTeamsMessage struct {
	ID      string `json:"id"`
	Time    string `json:"originalarrivaltime"`
	Compose string `json:"composetime"`
	From    string `json:"from"`
	Name    string `json:"imdisplayname"`
	Content string `json:"content"`
	Type    string `json:"messagetype"`
}

func (p *nativeTeams) messages(ctx context.Context, target, since string) ([]Message, error) {
	address, err := p.conversationURL(target)
	if err != nil {
		return nil, err
	}
	var cursor time.Time
	if since != "" {
		cursor, err = time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return nil, fmt.Errorf("invalid Teams message cursor")
		}
	}
	address += "/messages?pageSize=100&view=msnp24Equivalent"
	if !cursor.IsZero() {
		address += "&startTime=" + strconv.FormatInt(cursor.UnixMilli(), 10)
	}
	messages := make([]Message, 0)
	seen := map[string]bool{}
	seenMessages := map[string]bool{}
	totalBytes := 0
	for page := 0; page < 100; page++ {
		if seen[address] {
			return nil, fmt.Errorf("Teams repeated a message page")
		}
		seen[address] = true
		var result struct {
			Messages []nativeTeamsMessage `json:"messages"`
			Metadata struct {
				BackwardLink string `json:"backwardLink"`
			} `json:"_metadata"`
		}
		previousBase := p.base
		if _, err := p.request(ctx, http.MethodGet, address, nil, &result); err != nil {
			return nil, err
		}
		if previousBase != p.base {
			address = p.base + strings.TrimPrefix(address, previousBase)
		}
		if result.Messages == nil {
			return nil, fmt.Errorf("Teams returned no message list")
		}
		reachedCursor := false
		for _, raw := range result.Messages {
			if seenMessages[raw.ID] {
				continue
			}
			seenMessages[raw.ID] = true
			totalBytes += len(raw.Content)
			if totalBytes > teamsMaxResponse {
				return nil, fmt.Errorf("Teams message catch-up exceeds size limit")
			}
			timestamp := raw.Time
			if timestamp == "" {
				timestamp = raw.Compose
			}
			at, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil || raw.ID == "" {
				return nil, fmt.Errorf("Teams message is missing a valid id or timestamp")
			}
			if !cursor.IsZero() && !at.After(cursor) {
				reachedCursor = true
				continue
			}
			text := ""
			switch raw.Type {
			case "Text":
				text = raw.Content
			case "RichText/Html":
				text = cleanHTML(raw.Content)
			}
			from := raw.From[strings.LastIndex(raw.From, "/")+1:]
			from, err = url.PathUnescape(from)
			if err != nil {
				return nil, fmt.Errorf("Teams message has an invalid sender")
			}
			messages = append(messages, Message{ID: raw.ID, Timestamp: at.UTC().Format(teamsTimestamp),
				SenderID: from, SenderName: raw.Name, Text: text})
		}
		if since == "" || reachedCursor || len(result.Messages) == 0 || result.Metadata.BackwardLink == "" {
			sort.SliceStable(messages, func(i, j int) bool { return messages[i].Timestamp < messages[j].Timestamp })
			return messages, nil
		}
		next, err := url.Parse(result.Metadata.BackwardLink)
		if err != nil {
			return nil, fmt.Errorf("Teams returned invalid message pagination")
		}
		current, _ := url.Parse(address)
		next = current.ResolveReference(next)
		if next.Scheme != current.Scheme || next.Host != current.Host || next.Path != current.Path || next.User != nil {
			return nil, fmt.Errorf("Teams returned unsafe message pagination")
		}
		address = next.String()
	}
	return nil, fmt.Errorf("Teams message pagination exceeded limit")
}

func (p *nativeTeams) ListMessages(ctx context.Context, target, since string) ([]Message, error) {
	if since == "" {
		return nil, nil
	}
	return p.messages(ctx, target, since)
}

func (p *nativeTeams) LatestCursor(ctx context.Context, target string) (string, string, error) {
	messages, err := p.messages(ctx, target, "")
	if err != nil {
		return "", "", err
	}
	if len(messages) == 0 {
		return "", time.Unix(0, 0).UTC().Format(teamsTimestamp), nil
	}
	last := messages[len(messages)-1]
	return last.ID, last.Timestamp, nil
}

func (p *nativeTeams) Send(ctx context.Context, target, sender, content string) (string, error) {
	address, err := p.conversationURL(target)
	if err != nil {
		return "", err
	}
	var result struct {
		ID string `json:"id"`
	}
	headers, err := p.request(ctx, http.MethodPost, address+"/messages", map[string]string{
		"clientmessageid": strconv.FormatInt(time.Now().UnixMilli(), 10),
		"content":         teamsMessageHTML(sender, content), "messagetype": "RichText/Html",
		"contenttype": "text", "imdisplayname": p.account,
	}, &result)
	if err != nil {
		return "", err
	}
	if result.ID == "" && headers.Get("Location") != "" {
		location, err := url.Parse(headers.Get("Location"))
		if err == nil {
			result.ID = location.Path[strings.LastIndex(location.Path, "/")+1:]
		}
	}
	if result.ID == "" {
		return "", fmt.Errorf("Teams accepted the message but returned no message id; check the chat before retrying")
	}
	return result.ID, nil
}

var _ Provider = (*nativeTeams)(nil)
