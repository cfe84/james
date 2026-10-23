package memory

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Provenance separates the source of an assertion from its certainty.
type Provenance struct {
	Method    string `json:"method"`
	Source    string `json:"source"`
	Timestamp string `json:"timestamp,omitempty"`
}

// Link references another claim by stable ID, never by a mutable topic path.
type Link struct {
	Target   string `json:"target"`
	Relation string `json:"relation"`
	Title    string `json:"title,omitempty"`
}

// Claim is an individually qualified assertion in a hierarchical topic.
type Claim struct {
	ID            string       `json:"id"`
	Path          string       `json:"path"`
	Title         string       `json:"title"`
	Body          string       `json:"body"`
	BodyTruncated bool         `json:"body_truncated,omitempty"`
	Kind          string       `json:"kind"`
	Status        string       `json:"status"`
	Confidence    string       `json:"confidence"`
	Importance    string       `json:"importance"`
	RecordedAt    string       `json:"recorded_at"`
	VerifiedAt    string       `json:"verified_at,omitempty"`
	ReviewAfter   string       `json:"review_after,omitempty"`
	Provenance    []Provenance `json:"provenance"`
	Links         []Link       `json:"links"`
	Backlinks     []Link       `json:"backlinks"`
	MoreBacklinks bool         `json:"more_backlinks"`
	Revision      int64        `json:"revision"`
	Stale         bool         `json:"stale"`
}

const (
	claimColumns      = "id,path,title,body,kind,status,confidence,importance,recorded_at,verified_at,review_after,provenance,revision"
	claimKinds        = "fact,decision,observation,question,negative_result"
	claimStatuses     = "active,unresolved,disputed,disproven,superseded"
	claimConfidences  = "unverified,tentative,supported"
	claimImportances  = "critical,useful,incidental"
	provenanceMethods = "observed,inferred,user,document"
	linkRelations     = "related_to,supports,contradicts,supersedes"
)

func scanClaim(row interface{ Scan(...any) error }) (*Claim, error) {
	c := &Claim{Links: []Link{}}
	var provenance string
	err := row.Scan(&c.ID, &c.Path, &c.Title, &c.Body, &c.Kind, &c.Status, &c.Confidence, &c.Importance,
		&c.RecordedAt, &c.VerifiedAt, &c.ReviewAfter, &provenance, &c.Revision)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(provenance), &c.Provenance); err != nil {
		return nil, fmt.Errorf("decode claim provenance: %w", err)
	}
	c.Stale = c.ReviewAfter != "" && c.ReviewAfter <= time.Now().UTC().Format(time.RFC3339)
	return c, nil
}

func validChoice(value, choices string) bool {
	for _, choice := range strings.Split(choices, ",") {
		if value == choice {
			return true
		}
	}
	return false
}

func validClaimID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16 && id == strings.ToLower(id)
}

func normalizeTimestamp(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", fmt.Errorf("timestamp must be RFC3339: %w", err)
	}
	return t.UTC().Format(time.RFC3339), nil
}

func validateClaim(c *Claim) error {
	var err error
	c.Path, err = NormalizePath(c.Path)
	if err != nil {
		return err
	}
	if c.Path == "" {
		return errors.New("claims belong to a non-root topic")
	}
	for _, field := range []struct {
		name, value string
		max         int
	}{
		{"title", c.Title, 200}, {"body", c.Body, MaxBodyChars}, {"path", c.Path, MaxPathChars},
	} {
		if strings.TrimSpace(field.value) == "" || !utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) || utf8.RuneCountInString(field.value) > field.max {
			return fmt.Errorf("claim %s must be nonempty valid UTF-8 without NUL, at most %d characters", field.name, field.max)
		}
	}
	for _, field := range []struct{ name, value, choices string }{
		{"kind", c.Kind, claimKinds},
		{"status", c.Status, claimStatuses},
		{"confidence", c.Confidence, claimConfidences},
		{"importance", c.Importance, claimImportances},
	} {
		if !validChoice(field.value, field.choices) {
			return fmt.Errorf("invalid %s; choose %s", field.name, field.choices)
		}
	}
	for _, value := range []*string{&c.VerifiedAt, &c.ReviewAfter} {
		*value, err = normalizeTimestamp(*value)
		if err != nil {
			return err
		}
	}
	if c.VerifiedAt > time.Now().UTC().Format(time.RFC3339) {
		return errors.New("verified_at cannot be in the future")
	}
	if len(c.Provenance) == 0 || len(c.Provenance) > 8 || len(c.Links) > 16 {
		return errors.New("claims require 1–8 provenance entries and at most 16 links")
	}
	for i := range c.Provenance {
		p := &c.Provenance[i]
		if !validChoice(p.Method, provenanceMethods) || strings.TrimSpace(p.Source) == "" || !utf8.ValidString(p.Source) || strings.ContainsRune(p.Source, 0) || utf8.RuneCountInString(p.Source) > 500 {
			return errors.New("provenance requires method observed/inferred/user/document and source (1–500 characters)")
		}
		p.Timestamp, err = normalizeTimestamp(p.Timestamp)
		if err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, link := range c.Links {
		key := link.Target + ":" + link.Relation
		if !validClaimID(link.Target) || link.Target == c.ID || !validChoice(link.Relation, linkRelations) || seen[key] {
			return errors.New("links require distinct existing target IDs and relation related_to/supports/contradicts/supersedes; self-links are forbidden")
		}
		seen[key] = true
	}
	// Leave room for one-hop linked titles in the fixed retrieval budget.
	base := *c
	base.Links = nil
	base.Backlinks = nil
	raw, err := json.Marshal(base)
	if err != nil {
		return err
	}
	if utf8.RuneCount(raw) > 6000 {
		return errors.New("encoded claim metadata and body exceed 6000 characters; split the claim or shorten evidence")
	}
	return nil
}

func claimLinks(db interface {
	Query(string, ...any) (*sql.Rows, error)
}, id string) ([]Link, error) {
	rows, err := db.Query(`SELECT l.target,l.relation,c.title FROM memory_links l
		JOIN memory_claims c ON c.id=l.target WHERE l.source=? ORDER BY l.relation,l.target`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	links := []Link{}
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.Target, &link.Relation, &link.Title); err != nil {
			return nil, err
		}
		link.Title = boundedTitle(link.Title)
		links = append(links, link)
	}
	return links, rows.Err()
}

func boundedTitle(title string) string {
	runes := []rune(title)
	for len(runes) > 0 {
		encoded, _ := json.Marshal(string(runes))
		if len(encoded) <= 150 {
			if len(runes) < utf8.RuneCountInString(title) {
				return string(runes) + "…"
			}
			return title
		}
		runes = runes[:len(runes)-1]
	}
	return ""
}

func claimBacklinks(db interface {
	Query(string, ...any) (*sql.Rows, error)
}, c *Claim) error {
	rows, err := db.Query(`SELECT l.source,l.relation,c.title FROM memory_links l
		JOIN memory_claims c ON c.id=l.source WHERE l.target=? ORDER BY l.relation,l.source LIMIT 17`, c.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.Backlinks = []Link{}
	for rows.Next() {
		if len(c.Backlinks) == 16 {
			c.MoreBacklinks = true
			break
		}
		var link Link
		if err := rows.Scan(&link.Target, &link.Relation, &link.Title); err != nil {
			return err
		}
		link.Title = boundedTitle(link.Title)
		c.Backlinks = append(c.Backlinks, link)
	}
	return rows.Err()
}

// GetClaim returns current metadata, evidence and one-hop link titles.
func GetClaim(root, id string) (*Claim, error) {
	if !validClaimID(id) {
		return nil, errors.New("invalid claim ID")
	}
	db, err := open(root)
	if err != nil {
		return nil, err
	}
	defer db.close()
	c, err := scanClaim(db.QueryRow("SELECT "+claimColumns+" FROM memory_claims WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Links, err = claimLinks(db, id)
	if err == nil {
		err = claimBacklinks(db, c)
	}
	return c, err
}

// SetClaim creates a claim or replaces it only at the supplied current revision.
// Empty ID/revision zero creates; existing claims require a positive revision.
func SetClaim(root string, input Claim) (*Claim, error) {
	if input.BodyTruncated {
		return nil, errors.New("cannot replace a claim from a truncated summary; fetch the full claim first")
	}
	c := input
	c.Provenance = append([]Provenance(nil), input.Provenance...)
	c.RecordedAt = ""
	c.BodyTruncated = false
	c.Stale = false
	c.Backlinks = nil
	c.MoreBacklinks = false
	if c.ID == "" {
		if c.Revision != 0 {
			return nil, errors.New("new claims require revision zero")
		}
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil, err
		}
		c.ID = hex.EncodeToString(id[:])
	} else if !validClaimID(c.ID) || c.Revision < 1 {
		return nil, errors.New("updates require a valid ID and current revision")
	}
	if err := validateClaim(&c); err != nil {
		return nil, err
	}
	db, err := open(root)
	if err != nil {
		return nil, err
	}
	defer db.close()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow("SELECT count(*) FROM memory_nodes WHERE path=?", c.Path).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, errors.New("create the topic note before adding claims")
	}
	if c.Revision == 0 {
		c.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		if err := tx.QueryRow("SELECT recorded_at FROM memory_claims WHERE id=? AND revision=?", c.ID, c.Revision).Scan(&c.RecordedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, errors.New("claim revision conflict or claim not found; re-read before updating")
			}
			return nil, err
		}
	}
	c.Revision++
	if err := writeClaim(tx, &c); err != nil {
		return nil, err
	}
	if err := replaceLinks(tx, &c); err != nil {
		return nil, err
	}
	c.Links, err = claimLinks(tx, c.ID)
	if err != nil {
		return nil, err
	}
	c.MoreBacklinks = false
	if err := claimBacklinks(tx, &c); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	c.Stale = c.ReviewAfter != "" && c.ReviewAfter <= time.Now().UTC().Format(time.RFC3339)
	return &c, nil
}

func writeClaim(tx *sql.Tx, c *Claim) error {
	provenance, err := json.Marshal(c.Provenance)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO memory_claims (`+claimColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET path=excluded.path,title=excluded.title,body=excluded.body,
		kind=excluded.kind,status=excluded.status,confidence=excluded.confidence,importance=excluded.importance,
		verified_at=excluded.verified_at,review_after=excluded.review_after,provenance=excluded.provenance,revision=excluded.revision`,
		c.ID, c.Path, c.Title, c.Body, c.Kind, c.Status, c.Confidence, c.Importance,
		c.RecordedAt, c.VerifiedAt, c.ReviewAfter, string(provenance), c.Revision)
	return err
}

func replaceLinks(tx *sql.Tx, c *Claim) error {
	if _, err := tx.Exec("DELETE FROM memory_links WHERE source=?", c.ID); err != nil {
		return err
	}
	for _, link := range c.Links {
		if _, err := tx.Exec("INSERT INTO memory_links(source,target,relation) VALUES(?,?,?)", c.ID, link.Target, link.Relation); err != nil {
			return fmt.Errorf("link target must exist: %w", err)
		}
	}
	return nil
}

// DeleteClaim permanently erases a claim and its links, only at its current revision.
// Normal corrections should instead retain disproven/superseded records.
func DeleteClaim(root, id string, revision int64) error {
	if !validClaimID(id) || revision < 1 {
		return errors.New("claim deletion requires ID and current revision")
	}
	db, err := open(root)
	if err != nil {
		return err
	}
	defer db.close()
	result, err := db.Exec("DELETE FROM memory_claims WHERE id=? AND revision=?", id, revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return errors.New("claim revision conflict or claim not found")
	}
	return err
}

func claimSnapshot(db *sql.Tx) ([]*Claim, error) {
	rows, err := db.Query("SELECT " + claimColumns + " FROM memory_claims ORDER BY id")
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
		claims = append(claims, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range claims {
		c.Links, err = claimLinks(db, c.ID)
		if err != nil {
			return nil, err
		}
	}
	return claims, nil
}

func memorySnapshot(root string) ([]*Node, []*Claim, error) {
	db, err := open(root)
	if err != nil {
		return nil, nil, err
	}
	defer db.close()
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT " + nodeColumns + " FROM memory_nodes ORDER BY path")
	if err != nil {
		return nil, nil, err
	}
	nodes := []*Node{}
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		nodes = append(nodes, node)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	claims, err := claimSnapshot(tx)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return nodes, claims, nil
}

func copyMemory(root string, nodes []*Node, claims []*Claim) error {
	db, err := open(root)
	if err != nil {
		return err
	}
	defer db.close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeNodes(tx, nodes); err != nil {
		return err
	}
	for _, c := range claims {
		var exists int
		if err := tx.QueryRow("SELECT count(*) FROM memory_claims WHERE id=?", c.ID).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return errors.New("destination already contains a copied claim ID; refusing to overwrite")
		}
		if err := writeClaim(tx, c); err != nil {
			return err
		}
	}
	for _, c := range claims {
		if err := replaceLinks(tx, c); err != nil {
			return err
		}
	}
	return tx.Commit()
}
