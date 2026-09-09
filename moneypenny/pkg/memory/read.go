package memory

import (
	"database/sql"
	"fmt"
	"unicode/utf8"
)

const MaxReadCharacters = 64000

type Page struct {
	*Node
	Characters int  `json:"characters"`
	Offset     int  `json:"offset"`
	NextOffset *int `json:"next_offset,omitempty"`
}

// Read retrieves a bounded Unicode slice, so even unusually large imported
// notes remain readable through the bounded gadget protocol without truncation
// of stored knowledge. Offsets are Unicode code points, never bytes.
func Read(root, path string, offset, limit int) (*Page, error) {
	norm, err := NormalizePath(path)
	if err != nil {
		return nil, err
	}
	if offset < 0 || limit < 1 || limit > MaxReadCharacters {
		return nil, fmt.Errorf("offset must be nonnegative and limit must be 1–%d Unicode characters", MaxReadCharacters)
	}
	db, err := open(root, false)
	if err != nil {
		return nil, err
	}
	defer db.close()
	page := &Page{Node: &Node{}, Offset: offset}
	var hasNUL bool
	err = db.QueryRow(`SELECT path,title,description,substr(body,?,?),revision,length(body),instr(body,char(0))>0
		FROM memory_nodes WHERE path=?`, offset+1, limit, norm).Scan(
		&page.Path, &page.Title, &page.Description, &page.Body, &page.Revision, &page.Characters, &hasNUL)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// SQLite TEXT length/substr stop at NUL. Preserve unusual imported content
	// rather than silently hiding its remainder; ordinary Markdown stays SQL-paged.
	if hasNUL {
		var body string
		if err := db.QueryRow("SELECT body FROM memory_nodes WHERE path=?", norm).Scan(&body); err != nil {
			return nil, err
		}
		runes := []rune(body)
		page.Characters = len(runes)
		if offset <= len(runes) {
			end := offset + min(limit, len(runes)-offset)
			page.Body = string(runes[offset:end])
		}
	}
	if offset > page.Characters {
		return nil, fmt.Errorf("offset exceeds note length (%d characters)", page.Characters)
	}
	if offset == 0 && page.Description == "" {
		page.Description = deriveDescription(page.Body)
	}
	if next := offset + utf8.RuneCountInString(page.Body); next < page.Characters {
		page.NextOffset = &next
	}
	return page, nil
}

type Size struct {
	Path       string
	Characters int
}

// Oversized returns only paths and character counts, never descendant bodies.
func Oversized(root string) ([]Size, error) {
	db, err := open(root, false)
	if err != nil {
		return nil, err
	}
	defer db.close()
	rows, err := db.Query(`SELECT path,length(body),CASE WHEN instr(body,char(0))>0 THEN body ELSE '' END
		FROM memory_nodes WHERE length(body)>? OR instr(body,char(0))>0 ORDER BY path`, MaxBodyChars)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sizes []Size
	for rows.Next() {
		var size Size
		var nulBody string
		if err := rows.Scan(&size.Path, &size.Characters, &nulBody); err != nil {
			return nil, err
		}
		if nulBody != "" {
			size.Characters = utf8.RuneCountInString(nulBody)
		}
		if size.Characters > MaxBodyChars {
			sizes = append(sizes, size)
		}
	}
	return sizes, rows.Err()
}
