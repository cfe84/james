package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// teamsProvider drives Microsoft Teams via the tools exposed by
// `agency mcp teams`. Tool result payloads are JSON-in-text; because the exact
// shape emitted by agency is not contractually fixed here, parsing is
// defensive: we probe several common field names and tolerate wrapping objects.
type teamsProvider struct {
	mc *mcpClient

	selfMu   sync.Mutex
	selfID   string
	selfName string
	selfSet  bool
}

func newTeamsProvider(mc *mcpClient) *teamsProvider { return &teamsProvider{mc: mc} }

func (t *teamsProvider) Name() string { return "teams" }
func (t *teamsProvider) Caps() Caps   { return Caps{Search: true, ProvideID: true} }
func (t *teamsProvider) Close()       { t.mc.Close() }

var htmlTag = regexp.MustCompile(`<[^>]+>`)

// Search finds chats matching query using ListChats (topic filter) as the
// primary source and falling back to SearchTeamsMessages for chatIds.
func (t *teamsProvider) Search(ctx context.Context, query string) ([]Target, error) {
	out, err := t.mc.callTool(ctx, "ListChats", map[string]interface{}{"topic": query})
	if err == nil {
		if targets := parseChatTargets(out); len(targets) > 0 {
			return targets, nil
		}
	}
	// Fallback: natural-language message search yields chatIds.
	out2, err2 := t.mc.callTool(ctx, "SearchTeamsMessages", map[string]interface{}{"message": query})
	if err2 != nil {
		if err != nil {
			return nil, err
		}
		return nil, err2
	}
	if targets := parseChatTargets(out2); len(targets) > 0 {
		return targets, nil
	}
	return nil, nil
}

// Resolve fetches chat details for a directly-provided chatId.
func (t *teamsProvider) Resolve(ctx context.Context, targetID string) (Target, error) {
	out, err := t.mc.callTool(ctx, "GetChat", map[string]interface{}{"chatId": targetID})
	if err != nil {
		// The id may still be usable even if metadata lookup fails; surface a
		// minimal target rather than blocking the bind.
		return Target{ID: targetID, Label: targetID}, nil
	}
	if targets := parseChatTargets(out); len(targets) > 0 {
		return targets[0], nil
	}
	return Target{ID: targetID, Label: targetID}, nil
}

// ListMessages returns messages newer than sinceTS. It fetches a single page of
// the most-recent messages (Graph's max is 50); in the rare case that a chat
// receives more than that between polls, the older overflow is not delivered.
// The active-poll cadence (10s while a conversation is live) keeps this unlikely.
func (t *teamsProvider) ListMessages(ctx context.Context, targetID, sinceTS string) ([]Message, error) {
	args := map[string]interface{}{"chatId": targetID, "top": 50}
	out, err := t.mc.callTool(ctx, "ListChatMessages", args)
	if err != nil {
		return nil, err
	}
	msgs := parseMessages(out)
	// Filter to strictly-newer-than sinceTS. When sinceTS is empty, return
	// nothing but the caller records the newest id so history isn't replayed.
	var res []Message
	for _, m := range msgs {
		if sinceTS == "" {
			continue
		}
		if m.Timestamp > sinceTS {
			res = append(res, m)
		}
	}
	return res, nil
}

// Send posts content and returns the created message id.
func (t *teamsProvider) Send(ctx context.Context, targetID, content string) (string, error) {
	out, err := t.mc.callTool(ctx, "SendMessageToChat", map[string]interface{}{
		"chatId":  targetID,
		"content": content,
	})
	if err != nil {
		return "", err
	}
	return parseSentID(out), nil
}

// LatestCursor returns the id and timestamp of the most recent message on a
// target, used to initialize a channel's poll cursor without replaying history.
func (t *teamsProvider) LatestCursor(ctx context.Context, targetID string) (id, ts string, err error) {
	out, err := t.mc.callTool(ctx, "ListChatMessages", map[string]interface{}{"chatId": targetID, "top": 1})
	if err != nil {
		return "", "", err
	}
	msgs := parseMessages(out)
	if len(msgs) == 0 {
		return "", "", nil
	}
	// Messages come newest-first from Graph; pick the max timestamp defensively.
	newest := msgs[0]
	for _, m := range msgs[1:] {
		if m.Timestamp > newest.Timestamp {
			newest = m
		}
	}
	return newest.ID, newest.Timestamp, nil
}

// Self resolves the signed-in user's id via GetUserPresence (which, with no
// user argument, returns the signed-in account whose `id` is its AAD object id).
// The result is cached: identity is stable for the life of the process. On
// failure it returns an error so the caller can decide how to gate.
func (t *teamsProvider) Self(ctx context.Context) (id, name string, err error) {
	t.selfMu.Lock()
	if t.selfSet {
		id, name = t.selfID, t.selfName
		t.selfMu.Unlock()
		return id, name, nil
	}
	t.selfMu.Unlock()

	out, err := t.mc.callTool(ctx, "GetUserPresence", map[string]interface{}{})
	if err != nil {
		return "", "", err
	}
	recs := asRecords(out)
	for _, r := range recs {
		if sid := str(r, "id", "userId"); sid != "" {
			id = sid
			name = str(r, "displayName", "name")
			break
		}
	}
	if id == "" {
		// Presence returned but no parseable id: treat as a lookup failure so the
		// caller records degraded gating and we retry (do not cache the empty id).
		return "", "", fmt.Errorf("GetUserPresence returned no user id")
	}
	t.selfMu.Lock()
	t.selfID, t.selfName, t.selfSet = id, name, true
	t.selfMu.Unlock()
	return id, name, nil
}

// asRecords coerces a tool text payload into a slice of generic records,
// unwrapping common container keys ("value", "chats", "messages", "results",
// "items", "data").
func asRecords(text string) []map[string]interface{} {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var any interface{}
	// Use a streaming decoder so a single leading JSON value is parsed even if
	// the provider appended trailing non-JSON text (e.g. a telemetry trailer);
	// json.Unmarshal would reject trailing bytes.
	dec := json.NewDecoder(strings.NewReader(text))
	if err := dec.Decode(&any); err != nil {
		return nil
	}
	return recordsFrom(any)
}

func recordsFrom(v interface{}) []map[string]interface{} {
	switch t := v.(type) {
	case []interface{}:
		var out []map[string]interface{}
		for _, e := range t {
			if m, ok := e.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]interface{}:
		for _, key := range []string{"value", "chats", "messages", "results", "items", "data"} {
			if inner, ok := t[key]; ok {
				if recs := recordsFrom(inner); len(recs) > 0 {
					return recs
				}
			}
		}
		// A single record.
		return []map[string]interface{}{t}
	}
	return nil
}

func str(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func parseChatTargets(text string) []Target {
	recs := asRecords(text)
	var out []Target
	for _, r := range recs {
		id := str(r, "id", "chatId", "conversationId")
		if id == "" {
			continue
		}
		label := str(r, "topic", "displayName", "subject", "name")
		if label == "" {
			label = str(r, "lastMessagePreview", "preview")
		}
		if label == "" {
			label = id
		}
		out = append(out, Target{ID: id, Label: label, Detail: str(r, "chatType", "lastMessagePreview")})
	}
	return out
}

func parseMessages(text string) []Message {
	recs := asRecords(text)
	var out []Message
	for _, r := range recs {
		id := str(r, "id", "messageId")
		if id == "" {
			continue
		}
		m := Message{
			ID:        id,
			Timestamp: str(r, "createdDateTime", "timestamp", "createdAt"),
			Text:      messageText(r),
		}
		m.SenderID, m.SenderName = messageSender(r)
		out = append(out, m)
	}
	return out
}

func messageText(r map[string]interface{}) string {
	if body, ok := r["body"].(map[string]interface{}); ok {
		if s := str(body, "content", "text"); s != "" {
			return cleanHTML(s)
		}
	}
	if s := str(r, "content", "text", "preview"); s != "" {
		return cleanHTML(s)
	}
	return ""
}

func messageSender(r map[string]interface{}) (id, name string) {
	// Graph shape: from.user.{id,displayName}; agency may flatten to sender.
	if from, ok := r["from"].(map[string]interface{}); ok {
		if user, ok := from["user"].(map[string]interface{}); ok {
			return str(user, "id"), str(user, "displayName")
		}
		return str(from, "id"), str(from, "displayName")
	}
	if s, ok := r["sender"].(map[string]interface{}); ok {
		return str(s, "id"), str(s, "displayName", "name")
	}
	return str(r, "senderId"), str(r, "senderName", "senderDisplayName")
}

func cleanHTML(s string) string {
	s = htmlTag.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.TrimSpace(s)
}

func parseSentID(text string) string {
	recs := asRecords(text)
	for _, r := range recs {
		if id := str(r, "id", "messageId"); id != "" {
			return id
		}
	}
	return ""
}

// ensure teamsProvider satisfies Provider.
var _ Provider = (*teamsProvider)(nil)
