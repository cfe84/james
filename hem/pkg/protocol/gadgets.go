package protocol

import "encoding/json"

// GadgetRoute is the single JSON argument to the trusted daemon's "gadget route"
// request. The daemon supplies SourceSessionID from its authenticated endpoint.
type GadgetRoute struct {
	SourceSessionID string          `json:"source_session_id"`
	Method          string          `json:"method"`
	Data            json.RawMessage `json:"data"`
}
