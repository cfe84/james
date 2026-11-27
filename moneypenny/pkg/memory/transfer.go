package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

const MaxTransferBytes = 4 << 20

var errSnapshotTooLarge = errors.New("James memory exceeds the cross-host copy limit (4 MiB encoded snapshot)")

// Snapshot carries only authoritative session notes, claims and their links.
type Snapshot struct {
	Nodes  []*Node  `json:"nodes"`
	Claims []*Claim `json:"claims"`
}

func ExportSnapshot(root string) (json.RawMessage, error) {
	snapshot := Snapshot{Nodes: []*Node{}, Claims: []*Claim{}}
	if _, err := os.Stat(DatabasePath(root)); err == nil {
		// Allow for SQLite page overhead and indexes before applying the wire limit.
		snapshot.Nodes, snapshot.Claims, err = memorySnapshot(root, 4*MaxTransferBytes)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw, err := json.Marshal(snapshot)
	if len(raw) > MaxTransferBytes {
		return nil, errSnapshotTooLarge
	}
	return raw, err
}

func DecodeSnapshot(raw json.RawMessage) (*Snapshot, error) {
	if len(raw) > MaxTransferBytes {
		return nil, errSnapshotTooLarge
	}
	var snapshot Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil || snapshot.Nodes == nil || snapshot.Claims == nil {
		return nil, errors.New("invalid James memory snapshot")
	}
	for _, node := range snapshot.Nodes {
		if node == nil {
			return nil, errors.New("missing snapshot note")
		}
		path, err := NormalizePath(node.Path)
		if err != nil || path != node.Path {
			return nil, errors.New("invalid snapshot note path")
		}
	}
	for _, claim := range snapshot.Claims {
		if claim == nil || !validClaimID(claim.ID) || claim.Revision < 1 || claim.BodyTruncated {
			return nil, errors.New("invalid snapshot claim identity")
		}
		authored := *claim
		authored.RecordedAt, authored.Stale = "", false
		if err := validateClaim(&authored); err != nil {
			return nil, fmt.Errorf("invalid snapshot claim: %w", err)
		}
		if authored.Path != claim.Path {
			return nil, errors.New("noncanonical snapshot claim path")
		}
	}
	return &snapshot, nil
}

func (s *Snapshot) Empty() bool {
	return len(s.Nodes) == 0 && len(s.Claims) == 0
}

func (s *Snapshot) Restore(root string) error {
	return copyMemory(root, s.Nodes, s.Claims)
}
