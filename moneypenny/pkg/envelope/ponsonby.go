package envelope

// Ponsonby describes an explicitly attached shared memory store.
type Ponsonby struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Purpose      string `json:"purpose"`
	Introduction string `json:"introduction"`
	Publish      bool   `json:"publish"`
}

type PonsonbyStore struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Purpose      string        `json:"purpose"`
	Introduction string        `json:"introduction"`
	Agent        string        `json:"agent"`
	Model        string        `json:"model"`
	Jobs         []PonsonbyJob `json:"jobs,omitempty"`
}

type PonsonbyJob struct {
	ID       string `json:"id"`
	StoreID  string `json:"store_id"`
	SourceID string `json:"source_id"`
	Kind     string `json:"kind"`
	Body     string `json:"body,omitempty"`
	Status   string `json:"status"`
	Answer   string `json:"answer,omitempty"`
	Error    string `json:"error,omitempty"`
}

type PonsonbyRequest struct {
	Store PonsonbyStore `json:"store"`
	Job   PonsonbyJob   `json:"job"`
}
