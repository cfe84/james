package envelope

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const EventSessionAttention = "session_attention"

// SessionAttention is a durable request, independent of execution and read state.
// An empty kind records an explicit resolution while preserving its revision.
type SessionAttention struct {
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Revision  int64  `json:"revision"`
	Source    string `json:"source"`
	UpdatedAt string `json:"updated_at"`
}

func (a *SessionAttention) Active() bool { return a != nil && a.Kind != "" }

type SessionReport struct {
	Summary   string            `json:"summary,omitempty"`
	Attention *SessionAttention `json:"attention,omitempty"`
}

// AttentionUpdate leaves omitted fields unchanged. Resolving requires the
// current attention revision so a stale reader cannot dismiss a newer request.
type AttentionUpdate struct {
	SessionID string  `json:"session_id,omitempty"`
	Summary   *string `json:"summary,omitempty"`
	Kind      *string `json:"kind,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	Revision  *int64  `json:"revision,omitempty"`
}

func (u *AttentionUpdate) Validate() error {
	if u.Summary != nil {
		summary := strings.TrimSpace(*u.Summary)
		u.Summary = &summary
		if utf8.RuneCountInString(summary) > 240 {
			return fmt.Errorf("summary must contain at most 240 Unicode characters")
		}
	}
	if u.Kind == nil {
		if u.Summary == nil || u.Reason != "" {
			return fmt.Errorf("provide summary or an attention kind and reason")
		}
		return nil
	}
	switch *u.Kind {
	case "needs_answer", "needs_action", "failed":
		u.Reason = strings.TrimSpace(u.Reason)
		if u.Reason == "" || utf8.RuneCountInString(u.Reason) > 1000 {
			return fmt.Errorf("attention reason must contain 1-1000 Unicode characters")
		}
	case "":
		if u.Revision == nil || *u.Revision < 0 || u.Reason != "" {
			return fmt.Errorf("resolving attention requires its current revision and no reason")
		}
	default:
		return fmt.Errorf("attention kind must be needs_answer, needs_action or failed")
	}
	return nil
}
