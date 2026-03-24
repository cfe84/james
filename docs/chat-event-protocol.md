# Chat Event-Driven Protocol Design

## Overview

Replace the 3-second polling mechanism in chat sessions with real-time event notifications. When moneypenny detects changes (new activity, messages, status changes), it immediately sends notifications to hem, which broadcasts them to connected clients (TUI, web UI).

## Current State (Polling)

```
TUI Chat ──(every 3s)──> Hem ──> Moneypenny
                                    ↓
                    [Read from SQLite + activity buffer]
                                    ↓
TUI Chat <───────── Hem <─────── Response
```

**Problems:**
- 3-second latency for all updates
- Unnecessary load (4 concurrent requests per poll: history, schedules, subagents, activity)
- Wasted requests when nothing changed
- Poor user experience during agent execution

## Target State (Event-Driven)

```
Moneypenny Agent ──> Activity Event ──> Notification ──> Hem ──> Broadcast ──> TUI/Web
                                                            ↓
                                                    [Route by event type]
                                                            ↓
                                              Update only affected views
```

**Benefits:**
- Instant updates (millisecond latency)
- 60x reduction in polling (3s → fallback at 180s)
- Selective refresh (only affected data)
- Real-time "typing indicator" effect during agent execution

## Event Types

### 1. Chat Activity Events
**When:** Agent generates thinking, tool use, or text output during execution
**Frequency:** Multiple per second during active agent work
**Source:** `moneypenny/pkg/agent/agent.go:runStreaming()`

```go
type ChatActivityNotification struct {
    Event     string          `json:"event"`      // "chat.activity"
    SessionID string          `json:"session_id"`
    Data      ActivityData    `json:"data"`
}

type ActivityData struct {
    Events []ActivityEvent `json:"events"`  // Latest activity buffer snapshot
}

type ActivityEvent struct {
    Type      string `json:"type"`       // "thinking", "tool_use", "text"
    Summary   string `json:"summary"`    // Truncated content (150 chars)
    Timestamp string `json:"timestamp"`  // RFC3339
}
```

**Trigger Points:**
- After each `assistant` event parsed in `runStreaming()` (agent.go:173-202)
- Send snapshot of current activity buffer (last 30 events)

### 2. Chat Message Events
**When:** New conversation turn added (user prompt, assistant response, system message)
**Frequency:** 1-2 per agent turn completion
**Source:** `moneypenny/pkg/store/store.go:AddConversationTurn()`

```go
type ChatMessageNotification struct {
    Event     string      `json:"event"`      // "chat.message"
    SessionID string      `json:"session_id"`
    Data      MessageData `json:"data"`
}

type MessageData struct {
    Role      string `json:"role"`       // "user", "assistant", "system"
    Content   string `json:"content"`
    Timestamp string `json:"timestamp"`  // RFC3339
    TurnIndex int    `json:"turn_index"` // Position in conversation (1-based)
}
```

**Trigger Points:**
- `handler.go:186` - User prompt added
- `handler.go:318` - Assistant response added
- `handler.go:302` - System error message added
- `handler.go:1190` - Scheduled task notice added

### 3. Chat Status Events
**When:** Session status changes (ready ↔ working ↔ idle)
**Frequency:** 2 per agent execution (start working, finish idle)
**Source:** Existing `session.state_changed` notification

```go
type ChatStatusNotification struct {
    Event     string     `json:"event"`      // "chat.status"
    SessionID string     `json:"session_id"`
    Data      StatusData `json:"data"`
}

type StatusData struct {
    Status string `json:"status"`  // "ready", "working", "idle"
    Reason string `json:"reason"`  // "agent_started", "completed", "agent_error"
}
```

**Trigger Points:**
- `handler.go:307` - Agent completes or errors (already implemented)
- **NEW:** Beginning of `runAgent()` when status becomes "working"

### 4. Subagent Events
**When:** Subagent created or status changes
**Frequency:** Rare (user-initiated)
**Source:** `handler.go:handleCreateSession()`, subagent completion

```go
type SubagentNotification struct {
    Event     string        `json:"event"`      // "chat.subagent"
    SessionID string        `json:"session_id"` // Parent session
    Data      SubagentData  `json:"data"`
}

type SubagentData struct {
    SubagentID string `json:"subagent_id"`
    Name       string `json:"name"`
    Status     string `json:"status"`  // "ready", "working", "idle"
    Action     string `json:"action"`  // "created", "status_changed"
}
```

### 5. Schedule Events
**When:** Scheduled task created, executed, or deleted
**Frequency:** Rare (user-initiated or cron trigger)
**Source:** `handler.go:handleScheduleAgent()`, schedule execution

```go
type ScheduleNotification struct {
    Event     string       `json:"event"`      // "chat.schedule"
    SessionID string       `json:"session_id"`
    Data      ScheduleData `json:"data"`
}

type ScheduleData struct {
    ScheduleID string `json:"schedule_id"`
    Prompt     string `json:"prompt"`
    ScheduleAt string `json:"schedule_at"`  // RFC3339
    Action     string `json:"action"`       // "created", "executed", "deleted"
}
```

## Implementation Plan

### Phase 1: Moneypenny Event Generation

**Files:**
- `moneypenny/pkg/envelope/envelope.go` - Add new event constants
- `moneypenny/pkg/agent/agent.go` - Send activity notifications
- `moneypenny/pkg/store/store.go` - Send message notifications
- `moneypenny/pkg/handler/handler.go` - Send status/subagent/schedule notifications

**Changes:**

1. **Add Event Constants** (envelope.go)
```go
const (
    EventSessionStateChanged = "session.state_changed"  // Existing
    EventChatActivity        = "chat.activity"          // NEW
    EventChatMessage         = "chat.message"           // NEW
    EventChatStatus          = "chat.status"            // NEW
    EventChatSubagent        = "chat.subagent"          // NEW
    EventChatSchedule        = "chat.schedule"          // NEW
)
```

2. **Activity Notifications** (agent.go:runStreaming)
```go
// After parsing each assistant event and updating activity buffer:
if r.notifyWriter != nil {
    snapshot := buf.snapshot()
    r.notifyWriter.Send(EventChatActivity, sessionID, map[string]interface{}{
        "events": snapshot,
    })
}
```

3. **Message Notifications** (store.go:AddConversationTurn)
```go
func (s *Store) AddConversationTurn(sessionID, role, content string) error {
    // ... existing insert logic ...

    if s.notifyWriter != nil {
        s.notifyWriter.Send(EventChatMessage, sessionID, map[string]interface{}{
            "role":       role,
            "content":    content,
            "timestamp":  time.Now().Format(time.RFC3339),
            "turn_index": lastInsertID,
        })
    }
    return nil
}
```

4. **Status Notifications** (handler.go:runAgent)
```go
func (h *Handler) runAgent(sessionID string, params agent.RunParams) {
    // At start:
    h.store.UpdateSessionStatus(sessionID, store.StateWorking)
    if h.notifyWriter != nil {
        h.notifyWriter.Send(EventChatStatus, sessionID, map[string]string{
            "status": store.StateWorking,
            "reason": "agent_started",
        })
    }

    // ... agent execution ...

    // At completion (already exists, just add EventChatStatus):
    h.store.UpdateSessionStatus(sessionID, store.StateIdle)
    if h.notifyWriter != nil {
        h.notifyWriter.Send(EventChatStatus, sessionID, map[string]string{
            "status": store.StateIdle,
            "reason": "completed",
        })
    }
}
```

5. **Subagent Notifications** (handler.go:handleCreateSession)
```go
// After successful subagent creation:
if h.notifyWriter != nil && parentID != "" {
    h.notifyWriter.Send(EventChatSubagent, parentID, map[string]string{
        "subagent_id": sessionID,
        "name":        data.Name,
        "status":      store.StateReady,
        "action":      "created",
    })
}
```

6. **Schedule Notifications** (handler.go:handleScheduleAgent)
```go
// After scheduling task:
if h.notifyWriter != nil {
    h.notifyWriter.Send(EventChatSchedule, data.SessionID, map[string]string{
        "schedule_id": scheduleID,
        "prompt":      data.Prompt,
        "schedule_at": data.ScheduleAt,
        "action":      "created",
    })
}
```

### Phase 2: Hem Protocol Updates

**Files:**
- `hem/pkg/protocol/protocol.go` - Add notification types
- `hem/pkg/server/server.go` - Route notifications by event type

**Changes:**

1. **Notification Types** (protocol.go)
```go
// Add specific notification data structures:
type ChatActivityNotification struct {
    Events []ActivityEvent `json:"events"`
}

type ChatMessageNotification struct {
    Role      string `json:"role"`
    Content   string `json:"content"`
    Timestamp string `json:"timestamp"`
    TurnIndex int    `json:"turn_index"`
}

type ChatStatusNotification struct {
    Status string `json:"status"`
    Reason string `json:"reason"`
}

type ChatSubagentNotification struct {
    SubagentID string `json:"subagent_id"`
    Name       string `json:"name"`
    Status     string `json:"status"`
    Action     string `json:"action"`
}

type ChatScheduleNotification struct {
    ScheduleID string `json:"schedule_id"`
    Prompt     string `json:"prompt"`
    ScheduleAt string `json:"schedule_at"`
    Action     string `json:"action"`
}
```

2. **Broadcast Routing** (server.go)
```go
// When receiving notification from moneypenny transport:
func (s *Server) handleMoneypennyNotification(notification *transport.Notification) {
    // Convert to protocol.Response with appropriate verb/noun
    var verb, noun string
    switch notification.Event {
    case "chat.activity":
        verb, noun = "activity", "stream"
    case "chat.message":
        verb, noun = "message", "new"
    case "chat.status":
        verb, noun = "status", "changed"
    case "chat.subagent":
        verb, noun = "subagent", "changed"
    case "chat.schedule":
        verb, noun = "schedule", "changed"
    default:
        return // Unknown event
    }

    resp := &protocol.Response{
        Status:  protocol.StatusOK,
        Verb:    verb,
        Noun:    noun,
        Data:    notification.Data,
    }

    // Broadcast to all connected clients
    s.broadcast(resp)
}
```

### Phase 3: TUI Chat Updates

**Files:**
- `hem/pkg/ui/chat.go` - Event-driven updates

**Changes:**

1. **Replace Polling with Event Listener**
```go
const chatPollInterval = 180 * time.Second  // Fallback only (was 3s)

func (m chatModel) Init() tea.Cmd {
    cmds := []tea.Cmd{
        m.loadHistory(),
        m.loadSchedules(),
        m.loadSubagents(),
        m.loadActivity(),
        chatPollTick(),
    }

    // Start broadcast listener
    if broadcasts := m.client.broadcasts(); broadcasts != nil {
        cmds = append(cmds, listenForChatBroadcasts(broadcasts, m.sessionID))
    }

    return tea.Batch(cmds...)
}
```

2. **Broadcast Message Handler**
```go
type chatBroadcastMsg struct{ resp *protocol.Response }

func listenForChatBroadcasts(broadcasts <-chan *protocol.Response, sessionID string) tea.Cmd {
    return func() tea.Msg {
        if broadcasts == nil {
            return nil
        }
        for {
            resp, ok := <-broadcasts
            if !ok {
                return nil
            }
            // Only process broadcasts for this session
            if matchesSession(resp, sessionID) {
                return chatBroadcastMsg{resp: resp}
            }
        }
    }
}

func matchesSession(resp *protocol.Response, sessionID string) bool {
    // Extract session_id from notification data and compare
    var data map[string]interface{}
    json.Unmarshal(resp.Data, &data)
    sid, _ := data["session_id"].(string)
    return sid == sessionID
}
```

3. **Selective Refresh Handler**
```go
func (m chatModel) Update(msg tea.Msg) (chatModel, tea.Cmd) {
    switch msg := msg.(type) {
    case chatBroadcastMsg:
        cmds := []tea.Cmd{listenForChatBroadcasts(m.client.broadcasts(), m.sessionID)}

        // Route by verb/noun
        switch msg.resp.Verb {
        case "activity":
            // Refresh activity only (instant update during agent work)
            cmds = append(cmds, m.loadActivity())

        case "message":
            // Refresh conversation history
            cmds = append(cmds, m.loadHistory())

        case "status":
            // Update session status, maybe refresh history for final message
            cmds = append(cmds, m.loadHistory())

        case "subagent":
            // Refresh subagent list
            cmds = append(cmds, m.loadSubagents())

        case "schedule":
            // Refresh schedule list
            cmds = append(cmds, m.loadSchedules())
        }

        return m, tea.Batch(cmds...)

    case chatPollTickMsg:
        // Fallback polling (180s) - only if not receiving broadcasts
        if !m.sending && !m.loading && !m.polling {
            m.polling = true
            cmds := []tea.Cmd{
                m.loadHistory(),
                m.loadSchedules(),
                m.loadSubagents(),
                m.loadActivity(),
                chatPollTick(),
            }
            return m, tea.Batch(cmds...)
        }
        return m, chatPollTick()

    // ... existing handlers ...
    }
}
```

### Phase 4: Web UI Updates

**Files:**
- `qew/pkg/web/server.go` - Already has broadcast forwarding

**Changes:**
- Web UI already receives broadcasts via WebSocket (implemented in previous phase)
- Frontend JavaScript needs to handle chat-specific events:

```javascript
ws.onmessage = (event) => {
    const msg = JSON.parse(event.data);

    if (msg.verb === 'activity' && msg.noun === 'stream') {
        // Update activity display in real-time
        updateActivityStream(msg.data.events);
    } else if (msg.verb === 'message' && msg.noun === 'new') {
        // Append new message to conversation
        appendMessage(msg.data);
    } else if (msg.verb === 'status' && msg.noun === 'changed') {
        // Update session status indicator
        updateSessionStatus(msg.data.status);
    }
    // ... handle subagent, schedule events ...
};
```

## Performance Impact

### Current (Polling Every 3s)
- **Requests per minute:** 80 (4 endpoints × 20 polls)
- **Latency:** 0-3 seconds
- **Wasted requests:** ~95% (nothing changed)

### Event-Driven
- **Requests per minute:** ~0.3 (fallback only)
- **Latency:** <100ms
- **Wasted requests:** 0% (event-triggered only)

**Improvement:**
- **266x fewer requests** (80 → 0.3 per minute)
- **30x faster updates** (3s → <100ms)
- **Real-time experience** during agent execution

## Graceful Degradation

1. **Unix Socket Clients** (local hem CLI/TUI):
   - Receive instant broadcasts via MI6Sender
   - Fallback to 180s polling if broadcast channel unavailable

2. **MI6 Clients** (remote hem via MI6):
   - Receive instant broadcasts via MI6 transport
   - Fallback to 180s polling if connection lost

3. **Web Clients** (qew via WebSocket):
   - Receive instant broadcasts via WebSocket
   - Fallback to 180s XHR polling if WebSocket disconnects

## Testing Strategy

1. **Unit Tests:**
   - Notification generation in moneypenny
   - Broadcast routing in hem server
   - Event filtering in TUI

2. **Integration Tests:**
   - End-to-end: agent execution → notification → broadcast → TUI update
   - Multi-client: verify all connected clients receive broadcasts
   - Fallback: disconnect transport, verify polling resumes

3. **Performance Tests:**
   - Measure notification latency (moneypenny → TUI)
   - Verify no notification loss during high-frequency activity
   - Confirm ring buffer limits (30 events) work correctly

## Migration Path

1. **Phase 1:** Deploy moneypenny with notification generation (backward compatible)
2. **Phase 2:** Deploy hem with broadcast routing (existing chat still works via polling)
3. **Phase 3:** Deploy TUI with event handlers (automatic switchover to events)
4. **Phase 4:** Update web UI JavaScript (progressive enhancement)

No breaking changes - old clients continue polling, new clients use events.
