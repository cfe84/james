package memory

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	DefaultResults        = 10
	MaxResults            = 50
	MaxResponseCharacters = 16000
)

// Query applies deterministic filters; rejected and unresolved claims are included
// unless explicitly filtered. LinkedTo explores either incoming or outgoing edges.
type Query struct {
	Text           string `json:"text,omitempty"`
	Path           string `json:"path,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Status         string `json:"status,omitempty"`
	Confidence     string `json:"confidence,omitempty"`
	Importance     string `json:"importance,omitempty"`
	Method         string `json:"method,omitempty"`
	Source         string `json:"source,omitempty"`
	VerifiedBefore string `json:"verified_before,omitempty"`
	Stale          *bool  `json:"stale,omitempty"`
	LinkedTo       string `json:"linked_to,omitempty"`
	Relation       string `json:"relation,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
}

// Results is bounded by both count and serialized Unicode character count.
type Results[T any] struct {
	Items      []T    `json:"items"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type queryCursor struct {
	Key    string `json:"key"`
	Filter string `json:"filter"`
}

func pageStart(q Query, scope string) (Query, string, string, error) {
	if q.Limit == 0 {
		q.Limit = DefaultResults
	}
	if q.Limit < 1 || q.Limit > MaxResults {
		return q, "", "", fmt.Errorf("limit must be 1–%d", MaxResults)
	}
	if len(q.Cursor) > MaxResponseCharacters {
		return q, "", "", errors.New("invalid memory cursor")
	}
	var err error
	q.Path, err = NormalizePath(q.Path)
	if err != nil {
		return q, "", "", err
	}
	if !utf8.ValidString(q.Text) || !utf8.ValidString(q.Source) || utf8.RuneCountInString(q.Text) > MaxBodyChars || utf8.RuneCountInString(q.Source) > 500 || utf8.RuneCountInString(q.Path) > MaxPathChars {
		return q, "", "", errors.New("query text/source/path exceeds bounds or is not valid UTF-8")
	}
	cursor := q.Cursor
	q.Cursor = ""
	identity, err := json.Marshal(struct {
		Scope string
		Query Query
	}{scope, q})
	if err != nil {
		return q, "", "", err
	}
	hash := sha256.Sum256(identity)
	filter := hex.EncodeToString(hash[:])
	last := ""
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		var c queryCursor
		if err != nil || json.Unmarshal(data, &c) != nil || c.Filter != filter {
			return q, "", "", errors.New("invalid cursor or query changed; restart the query")
		}
		last = c.Key
	}
	return q, last, filter, nil
}

func nextCursor(key, filter string) string {
	b, _ := json.Marshal(queryCursor{Key: key, Filter: filter})
	return base64.RawURLEncoding.EncodeToString(b)
}

func appendResult[T any](page *Results[T], item T, key, filter string) (bool, error) {
	page.Items = append(page.Items, item)
	page.HasMore = true
	page.NextCursor = nextCursor(key, filter)
	data, err := json.Marshal(page)
	if err != nil {
		return false, err
	}
	if utf8.RuneCount(data) > MaxResponseCharacters-256 {
		page.Items = page.Items[:len(page.Items)-1]
		if len(page.Items) == 0 {
			return false, errors.New("memory item exceeds response budget; narrow the request")
		}
		return false, nil
	}
	return true, nil
}

func preview(body string, limit int) string {
	runes := []rune(body)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return body
}

// QueryClaims returns evidence-bearing summaries in stable ID order.
func QueryClaims(root string, input Query) (*Results[*Claim], error) {
	q, last, filter, err := pageStart(input, "claims")
	if err != nil {
		return nil, err
	}
	for _, field := range []struct{ value, choices string }{
		{q.Kind, claimKinds},
		{q.Status, claimStatuses},
		{q.Confidence, claimConfidences},
		{q.Importance, claimImportances},
		{q.Method, provenanceMethods},
		{q.Relation, linkRelations},
	} {
		if field.value != "" && !validChoice(field.value, field.choices) {
			return nil, fmt.Errorf("invalid query filter %q", field.value)
		}
	}
	if q.LinkedTo != "" && !validClaimID(q.LinkedTo) {
		return nil, errors.New("invalid linked_to claim ID")
	}
	if q.Relation != "" && q.LinkedTo == "" {
		return nil, errors.New("relation requires linked_to")
	}
	q.VerifiedBefore, err = normalizeTimestamp(q.VerifiedBefore)
	if err != nil {
		return nil, err
	}
	where := []string{"id>?"}
	args := []any{last}
	if q.Path != "" {
		where = append(where, "(path=? OR substr(path,1,length(?))=?)")
		args = append(args, q.Path, q.Path+"/", q.Path+"/")
	}
	if q.Text != "" {
		where = append(where, "memory_contains(title || char(10) || body,?)")
		args = append(args, q.Text)
	}
	for _, field := range []struct{ column, value string }{
		{"kind", q.Kind}, {"status", q.Status}, {"confidence", q.Confidence}, {"importance", q.Importance},
	} {
		if field.value != "" {
			where = append(where, field.column+"=?")
			args = append(args, field.value)
		}
	}
	if q.Method != "" || q.Source != "" {
		where = append(where, `EXISTS (SELECT 1 FROM json_each(memory_claims.provenance) p WHERE
			(?='' OR json_extract(p.value,'$.method')=?) AND
			(?='' OR memory_contains(json_extract(p.value,'$.source'),?)))`)
		args = append(args, q.Method, q.Method, q.Source, q.Source)
	}
	if q.VerifiedBefore != "" {
		where = append(where, "verified_at!='' AND verified_at<?")
		args = append(args, q.VerifiedBefore)
	}
	if q.Stale != nil {
		predicate := "(review_after!='' AND review_after<=strftime('%Y-%m-%dT%H:%M:%SZ','now'))"
		if !*q.Stale {
			predicate = "NOT " + predicate
		}
		where = append(where, predicate)
	}
	if q.LinkedTo != "" {
		where = append(where, `EXISTS (SELECT 1 FROM memory_links l WHERE
			((l.source=memory_claims.id AND l.target=?) OR (l.target=memory_claims.id AND l.source=?))
			AND (?='' OR l.relation=?))`)
		args = append(args, q.LinkedTo, q.LinkedTo, q.Relation, q.Relation)
	}
	db, err := open(root)
	if err != nil {
		return nil, err
	}
	defer db.close()
	args = append(args, q.Limit+1)
	rows, err := db.Query("SELECT "+claimColumns+" FROM memory_claims WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	claims := []*Claim{}
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		c.BodyTruncated = utf8.RuneCountInString(c.Body) > 240
		c.Body = preview(c.Body, 240)
		claims = append(claims, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	page := &Results[*Claim]{Items: []*Claim{}}
	lastIncluded := ""
	for i, c := range claims {
		if i == q.Limit {
			page.HasMore = true
			break
		}
		c.Links, err = claimLinks(db, c.ID)
		if err != nil {
			return nil, err
		}
		if err := claimBacklinks(db, c); err != nil {
			return nil, err
		}
		ok, err := appendResult(page, c, c.ID, filter)
		if err != nil {
			return nil, err
		}
		if !ok {
			page.HasMore = true
			break
		}
		lastIncluded = c.ID
		page.HasMore = false
	}
	if page.HasMore {
		page.NextCursor = nextCursor(lastIncluded, filter)
	} else {
		page.NextCursor = ""
	}
	return page, nil
}

// NoteSummary deliberately omits full bodies, including oversized imported notes.
type NoteSummary struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Revision    int64  `json:"revision"`
	Characters  int    `json:"characters"`
}

// QueryNotes bounds gadget search and immediate-child browsing without changing
// internal tree snapshots used by copying and the existing memory editors.
func QueryNotes(root string, input Query, children bool) (*Results[NoteSummary], error) {
	scope := "notes-search"
	if children {
		scope = "notes-children"
	}
	q, last, filter, err := pageStart(input, scope)
	if err != nil {
		return nil, err
	}
	if !children && strings.TrimSpace(q.Text) == "" {
		return nil, errors.New("search query is empty")
	}
	where := []string{"path>?"}
	args := []any{last}
	prefix := ""
	if q.Path != "" {
		prefix = q.Path + "/"
	}
	if children {
		where = append(where, `path!='' AND substr(path,1,length(?))=? AND instr(substr(path,length(?)+1),'/')=0`)
		args = append(args, prefix, prefix, prefix)
	} else {
		where = append(where, "memory_contains(path || char(10) || title || char(10) || description || char(10) || body,?)")
		args = append(args, q.Text)
	}
	if !children && q.Path != "" {
		where = append(where, "(path=? OR substr(path,1,length(?))=?)")
		args = append(args, q.Path, prefix, prefix)
	}
	// The root has an empty key but still counts as a consumed cursor.
	if !children && input.Cursor == "" {
		where[0] = "path>=?"
	}
	db, err := open(root)
	if err != nil {
		return nil, err
	}
	defer db.close()
	args = append(args, q.Limit+1)
	rows, err := db.Query(`SELECT path,substr(title,1,200),substr(CASE WHEN description='' THEN body ELSE description END,1,240),revision,length(replace(body,char(0),char(1)))
		FROM memory_nodes WHERE `+strings.Join(where, " AND ")+" ORDER BY path COLLATE BINARY LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &Results[NoteSummary]{Items: []NoteSummary{}}
	lastIncluded := ""
	for rows.Next() {
		var n NoteSummary
		if err := rows.Scan(&n.Path, &n.Title, &n.Description, &n.Revision, &n.Characters); err != nil {
			return nil, err
		}
		if len(page.Items) == q.Limit {
			page.HasMore = true
			break
		}
		ok, err := appendResult(page, n, n.Path, filter)
		if err != nil {
			return nil, err
		}
		if !ok {
			page.HasMore = true
			break
		}
		lastIncluded = n.Path
		page.HasMore = false
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if page.HasMore {
		page.NextCursor = nextCursor(lastIncluded, filter)
	} else {
		page.NextCursor = ""
	}
	return page, nil
}
