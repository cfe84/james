package envelope

// GetDiagnosticsData opts into bounded, content-free aggregation for one exact
// session ID. A scan requires both session_id and scan_session.
type GetDiagnosticsData struct {
	SessionID   string `json:"session_id,omitempty"`
	ScanSession bool   `json:"scan_session,omitempty"`
}

type GetDiagnosticsResponse struct {
	PID          int                    `json:"pid"`
	Runtime      RuntimeDiagnostics     `json:"runtime"`
	Process      ProcessDiagnostics     `json:"process"`
	Files        DiagnosticsFiles       `json:"files"`
	Database     DatabaseDiagnostics    `json:"database"`
	Dispatcher   *DispatcherDiagnostics `json:"dispatcher,omitempty"`
	Session      *SessionDiagnostics    `json:"session,omitempty"`
	SessionError string                 `json:"session_error,omitempty"`
}

// RuntimeDiagnostics counters describe the entire Go process, not allocations
// attributable to an individual request. Values are sampled without forcing GC.
type RuntimeDiagnostics struct {
	HeapAllocBytes    uint64 `json:"heap_alloc_bytes"`
	HeapObjects       uint64 `json:"heap_objects"`
	HeapReleasedBytes uint64 `json:"heap_released_bytes"`
	TotalAllocBytes   uint64 `json:"total_alloc_bytes"`
	GCCycles          uint64 `json:"gc_cycles"`
	Goroutines        uint64 `json:"goroutines"`
}

// Nil memory values are unavailable, not zero. RSS includes resident non-Go
// memory; Windows private bytes are committed private memory, not RSS.
type ProcessDiagnostics struct {
	OS           string  `json:"os"`
	RSSBytes     *uint64 `json:"rss_bytes,omitempty"`
	PrivateBytes *uint64 `json:"private_bytes,omitempty"`
	Error        string  `json:"error,omitempty"`
}

type DiagnosticsFile struct {
	Bytes *int64 `json:"bytes,omitempty"`
	Error string `json:"error,omitempty"`
}

type DiagnosticsFiles struct {
	Database DiagnosticsFile `json:"database"`
	WAL      DiagnosticsFile `json:"wal"`
	Log      DiagnosticsFile `json:"log"`
}

type DatabaseDiagnostics struct {
	MaxOpenConnections int   `json:"max_open_connections"`
	OpenConnections    int   `json:"open_connections"`
	InUse              int   `json:"in_use"`
	Idle               int   `json:"idle"`
	WaitCount          int64 `json:"wait_count"`
	WaitDurationNS     int64 `json:"wait_duration_ns"`
}

type DispatcherDiagnostics struct {
	Commands    WorkerDiagnostics `json:"commands"`
	Reads       WorkerDiagnostics `json:"reads"`
	Diagnostics WorkerDiagnostics `json:"diagnostics"`
}

// Active includes response encoding/writing. Queue and active values are
// approximate concurrent snapshots, not an atomic view of all workers.
type WorkerDiagnostics struct {
	Queued   int   `json:"queued"`
	Capacity int   `json:"capacity"`
	Active   int64 `json:"active"`
	Workers  int   `json:"workers"`
}

// PayloadDiagnostics measures only prompt/content UTF-8 bytes, not SQLite
// storage overhead or the size of serialized protocol responses.
type PayloadDiagnostics struct {
	Rows       int64 `json:"rows"`
	TotalBytes int64 `json:"total_bytes"`
	MaxBytes   int64 `json:"max_bytes"`
}

type ScheduleDiagnostics struct {
	PayloadDiagnostics
	Pending int64 `json:"pending"`
	Running int64 `json:"running"`
	Done    int64 `json:"done"`
	Other   int64 `json:"other"`
}

type SessionDiagnostics struct {
	SessionID    string              `json:"session_id"`
	Status       string              `json:"status"`
	Schedules    ScheduleDiagnostics `json:"schedules"`
	Conversation PayloadDiagnostics  `json:"conversation"`
	Queue        PayloadDiagnostics  `json:"queue"`
}
