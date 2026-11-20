package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"james/moneypenny/pkg/artifact"
)

// An empty caller denotes an operator route. Agent routes always supply a bound caller.
const artifactMember = `(? = '' OR EXISTS (SELECT 1 FROM sessions WHERE session_id = ? AND sessions.project_id = project_artifacts.project_id))`

func (s *Store) ListProjectArtifacts(project, caller string) ([]artifact.Metadata, error) {
	rows, err := s.db.Query(`SELECT metadata FROM project_artifacts WHERE project_id = ? AND `+artifactMember+` ORDER BY json_extract(metadata, '$.sent_at'), id`, project, caller, caller)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []artifact.Metadata{}
	for rows.Next() {
		var raw []byte
		var meta artifact.Metadata
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, err
		}
		items = append(items, meta)
	}
	return items, rows.Err()
}

func (s *Store) ReadProjectArtifact(project, caller string, request artifact.ReadRequest) (artifact.Chunk, error) {
	var chunk artifact.Chunk
	if request.Offset < 0 || request.Offset > artifact.MaxFileSize {
		return chunk, fmt.Errorf("invalid artifact offset")
	}
	length := artifact.ChunkSize
	if request.MetadataOnly {
		length = 0
	}
	var raw []byte
	err := s.db.QueryRow(`SELECT metadata, substr(content, ?, ?) FROM project_artifacts WHERE project_id = ? AND id = ? AND `+artifactMember,
		request.Offset+1, length, project, request.ID, caller, caller).Scan(&raw, &chunk.Content)
	if err == sql.ErrNoRows {
		return chunk, fmt.Errorf("project artifact not found")
	}
	if err != nil {
		return chunk, err
	}
	if err := json.Unmarshal(raw, &chunk.Artifact); err != nil {
		return chunk, err
	}
	if request.Offset > chunk.Artifact.Size {
		return chunk, fmt.Errorf("invalid artifact offset")
	}
	return chunk, nil
}

func (s *Store) SaveProjectArtifact(ctx context.Context, caller string, meta artifact.Metadata, content []byte, replace bool) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	var result sql.Result
	if replace {
		result, err = s.db.ExecContext(ctx, `UPDATE project_artifacts SET metadata = ?, content = CAST(? AS BLOB)
			WHERE project_id = ? AND id = ? AND `+artifactMember+`
			AND ? + (SELECT COALESCE(SUM(length(other.content)), 0) FROM project_artifacts other
				WHERE other.project_id = project_artifacts.project_id AND other.id != project_artifacts.id) <= ?`,
			string(raw), content, meta.ProjectID, meta.ID, caller, caller, len(content), artifact.MaxSessionSize)
	} else {
		result, err = s.db.ExecContext(ctx, `INSERT INTO project_artifacts (project_id, id, metadata, content)
			SELECT p.id, ?, ?, CAST(? AS BLOB) FROM projects p
			WHERE p.id = ? AND EXISTS (SELECT 1 FROM sessions WHERE session_id = ? AND project_id = p.id)
			AND (SELECT COUNT(*) FROM project_artifacts WHERE project_id = p.id) < ?
			AND ? + (SELECT COALESCE(SUM(length(content)), 0) FROM project_artifacts WHERE project_id = p.id) <= ?`,
			meta.ID, string(raw), content, meta.ProjectID, caller, artifact.MaxFiles, len(content), artifact.MaxSessionSize)
	}
	return requireArtifactChange(result, err, "project membership changed, artifact is missing, or project artifact capacity exceeded")
}

func (s *Store) DeleteProjectArtifact(project, caller, id string) error {
	result, err := s.db.Exec(`DELETE FROM project_artifacts WHERE project_id = ? AND id = ? AND `+artifactMember, project, id, caller, caller)
	return requireArtifactChange(result, err, "project artifact not found")
}

func requireArtifactChange(result sql.Result, err error, missing string) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return fmt.Errorf("%s", missing)
	}
	return err
}
