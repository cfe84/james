package store

import "fmt"

const projectCategoriesQuery = `SELECT '' AS name UNION SELECT category AS name FROM projects`

// CategoryOrder returns current category names in their saved display order.
func (s *Store) CategoryOrder() ([]string, error) {
	rows, err := s.db.Query(`SELECT c.name FROM (` + projectCategoriesQuery + `) c
		LEFT JOIN project_category_defaults d ON d.name = c.name
		ORDER BY d.sort_order IS NULL, d.sort_order, c.name`)
	if err != nil {
		return nil, fmt.Errorf("list category order: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan category order: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// ReorderProjects saves a complete ordering of project IDs without changing categories.
func (s *Store) ReorderProjects(ids []string) error {
	return s.reorderProjects(`SELECT id FROM projects`,
		`UPDATE projects SET sort_order = ? WHERE id = ?`, ids)
}

// ReorderCategories saves a complete ordering, including empty-name Uncategorized.
func (s *Store) ReorderCategories(names []string) error {
	return s.reorderProjects(projectCategoriesQuery,
		`INSERT INTO project_category_defaults (name, sort_order) VALUES (?2, ?1)
		 ON CONFLICT(name) DO UPDATE SET sort_order = excluded.sort_order`, names)
}

func (s *Store) reorderProjects(query, update string, order []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin reorder: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(query)
	if err != nil {
		return fmt.Errorf("read current order: %w", err)
	}
	current := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan current order: %w", err)
		}
		current[name] = true
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return fmt.Errorf("read current order: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close current order: %w", closeErr)
	}
	if len(order) != len(current) {
		return fmt.Errorf("project list changed; refresh before reordering")
	}
	for _, name := range order {
		if !current[name] {
			return fmt.Errorf("invalid or duplicate reorder entry %q; refresh before reordering", name)
		}
		delete(current, name)
	}
	for rank, name := range order {
		if _, err := tx.Exec(update, rank, name); err != nil {
			return fmt.Errorf("save order: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit order: %w", err)
	}
	return nil
}
