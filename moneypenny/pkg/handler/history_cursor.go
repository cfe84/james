package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"james/moneypenny/pkg/envelope"
	"james/moneypenny/pkg/store"
)

const maxConversationResponseBytes = 1 << 20

type historyCursor struct {
	Version    int   `json:"v"`
	Generation int64 `json:"g"`
	Offset     int   `json:"o"`
	TurnID     int64 `json:"t,omitempty"`
	Chunk      int   `json:"c,omitempty"`
}

func encodeHistoryCursor(c historyCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeHistoryCursor(value string, generation int64) (historyCursor, error) {
	var c historyCursor
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Offset < 0 || c.Chunk < 0 {
		return c, fmt.Errorf("malformed history cursor")
	}
	if c.Generation != generation {
		return c, fmt.Errorf("stale history cursor")
	}
	return c, nil
}

// boundedConversation selects a deterministic chronological page whose encoded
// JSON representation fits maxBytes. JSON marshaling is intentional here:
// len(Content) is not the wire size when UTF-8 escaping is involved.
func boundedConversation(sessionID string, turns []*store.ConversationTurn, total int, revision, generation int64, offset, chunkStart int, maxBytes int, reconcile bool) ([]envelope.ConversationTurn, string, bool, bool, error) {
	if offset < 0 || offset > total || chunkStart < 0 {
		return nil, "", false, false, fmt.Errorf("invalid history cursor position")
	}
	if chunkStart > 0 && (len(turns) == 0 || chunkStart > len(turns[0].Content) || !utf8.RuneStart(turns[0].Content[chunkStart])) {
		return nil, "", false, false, fmt.Errorf("invalid history cursor position")
	}
	if maxBytes <= 0 || maxBytes > 4*1024*1024 {
		maxBytes = maxConversationResponseBytes
	}
	out := make([]envelope.ConversationTurn, 0, len(turns))
	hasMore := false
	nextCursor := ""
	for i, t := range turns {
		candidate := envelope.ConversationTurn{ID: t.ID, Role: t.Role, Content: t.Content, SourceSessionID: t.SourceSessionID, SourceName: t.SourceName, CreatedAt: t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")}
		if i == 0 && chunkStart > 0 {
			candidate.Content = utf8Prefix(t.Content[chunkStart:], len(t.Content)-chunkStart)
		}
		test := append(append([]envelope.ConversationTurn(nil), out...), candidate)
		next := ""
		if offset+i+1 < total {
			next = encodeHistoryCursor(historyCursor{Version: 1, Generation: generation, Offset: offset + i + 1, TurnID: t.ID})
		}
		var payload interface{}
		if reconcile {
			payload = envelope.SessionReconcile{SessionID: sessionID, Conversation: test, Total: total, Revision: revision, Generation: generation, NextCursor: next, HasMore: next != ""}
		} else {
			payload = envelope.SessionConversation{SessionID: sessionID, Conversation: test, Total: total, Revision: revision, Generation: generation, NextCursor: next, HasMore: next != ""}
		}
		wire, _ := json.Marshal(payload)
		if len(wire) > maxBytes && len(out) > 0 {
			hasMore = true
			break
		}
		if len(wire) > maxBytes {
			if i == 0 {
				// Find the largest rune-aligned content prefix that fits.
				content := t.Content
				start := chunkStart
				if start > len(content) {
					start = len(content)
				}
				best := ""
				found := false
				for end := start; end <= len(content); {
					piece := content[start:end]
					candidate.Content = piece
					test = append(append([]envelope.ConversationTurn(nil), out...), candidate)
					nextPosition := historyCursor{Version: 1, Generation: generation, Offset: offset + i, TurnID: t.ID, Chunk: start + len(piece)}
					if end == len(content) {
						nextPosition.Offset++
						nextPosition.Chunk = 0
					}
					pieceCursor := encodeHistoryCursor(nextPosition)
					pieceMore := nextPosition.Offset < total
					if reconcile {
						payload = envelope.SessionReconcile{SessionID: sessionID, Conversation: test, Total: total, Revision: revision, Generation: generation, NextCursor: pieceCursor, HasMore: pieceMore}
					} else {
						payload = envelope.SessionConversation{SessionID: sessionID, Conversation: test, Total: total, Revision: revision, Generation: generation, NextCursor: pieceCursor, HasMore: pieceMore}
					}
					wire, _ = json.Marshal(payload)
					if len(wire) > maxBytes {
						break
					}
					best = piece
					found = true
					if end == len(content) {
						break
					}
					_, size := utf8.DecodeRuneInString(content[end:])
					end += size
				}
				if found {
					candidate.Content = best
					out = append(out, candidate)
					hasMore = true
					nextCursor = encodeHistoryCursor(historyCursor{Version: 1, Generation: generation, Offset: offset + i, TurnID: t.ID, Chunk: start + len(best)})
				} else {
					return nil, "", false, false, fmt.Errorf("max_bytes is too small for a conversation response")
				}
			}
			break
		}
		out = test
	}
	if offset+len(out) < total {
		hasMore = true
	}
	if hasMore && len(out) > 0 {
		if nextCursor != "" {
			return out, nextCursor, hasMore, len(out) < len(turns), nil
		}
		last := out[len(out)-1]
		nextCursor = encodeHistoryCursor(historyCursor{Version: 1, Generation: generation, Offset: offset + len(out), TurnID: last.ID})
	}
	return out, nextCursor, hasMore, len(out) < len(turns), nil
}

func cursorPosition(value string, generation int64) (int, int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, 0, nil
	}
	c, err := decodeHistoryCursor(value, generation)
	if err != nil {
		return 0, 0, err
	}
	return c.Offset, c.Chunk, nil
}

func cursorOffset(value string, generation int64) (int, error) {
	offset, _, err := cursorPosition(value, generation)
	return offset, err
}

func utf8Prefix(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
