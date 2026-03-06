# James Architecture

## MI6 - Agent Communication Relay

MI6 is a transport layer for remote agents to communicate via sessions. It consists of two binaries: `mi6-client` (local) and `mi6-server` (remote/container).

### Project Structure

```
mi6/
├── go.mod
├── cmd/
│   ├── mi6-client/main.go
│   └── mi6-server/main.go
├── pkg/
│   ├── protocol/     # Wire protocol messages and codec
│   ├── auth/          # SSH key auth, challenge-response, ECDH key exchange
│   ├── session/       # Server-side session management
│   └── transport/     # Encrypted connection wrapper
├── internal/
│   ├── batch/         # Stdin batching for client
│   └── config/        # CLI flags and configuration
└── Dockerfile
```

### Key Technical Decisions

1. **Transport**: Raw TCP with custom binary framing (length-prefixed). Simpler than HTTP/WebSocket for a stdio relay.

2. **Authentication**: SSH keys (RSA + ECDSA) for identity via challenge-response. Uses `golang.org/x/crypto/ssh` for key parsing and signature verification.

3. **Encryption**: Ephemeral X25519 ECDH key exchange during auth handshake, deriving AES-256-GCM symmetric key via HKDF. Provides forward secrecy.

4. **Wire Protocol**: Binary framed messages. Pre-auth messages are plaintext; post-auth encrypted with AES-256-GCM. Frame: `[length:4][nonce:12][encrypted_payload][tag:16]`.

5. **Sessions**: Lazy creation (first client join creates session). In-memory only (no persistence). Server broadcasts data to all OTHER connected clients in the same session.

6. **Batching**: Client batches stdin with triple trigger: newline, buffer size (4KB), or idle timeout (100ms).

7. **authorized_keys**: Standard OpenSSH format, reloaded on SIGHUP.

### Auth Flow

```
Client                              Server
  │── MsgAuth {public_key} ──────────>│  (server checks authorized_keys)
  │<── MsgAuthChallenge {challenge,   │  (generates challenge + ephemeral ECDH key)
  │     server_ecdh_pub} ────────────│
  │── MsgAuthResponse {signature,     │  (client signs challenge, generates ECDH key)
  │     client_ecdh_pub} ───────────>│  (server verifies, derives shared secret)
  │<── MsgAuthOK ────────────────────│
  │     [encrypted from here]         │
```

### Implementation Phases

1. Protocol & codec
2. Auth package (key loading, challenge-response, ECDH)
3. Transport layer (encrypted conn)
4. Session management (server-side)
5. Stdin batcher (client-side)
6. Client binary
7. Server binary
8. Dockerfile

## Moneypenny - Agent Session Manager

Moneypenny is a per-host daemon that manages Claude Code agent sessions. It receives JSON commands via stdio (directly or through MI6) and orchestrates agent subprocesses.

### Project Structure

```
moneypenny/
├── go.mod
├── cmd/moneypenny/main.go      # Entry point: stdio/MI6 modes, key management
├── pkg/
│   ├── envelope/               # JSON protocol types (command/response envelopes, error codes)
│   ├── store/                  # SQLite persistence (sessions, conversation history)
│   ├── agent/                  # Agent subprocess runner (claude CLI invocation)
│   └── handler/                # Command dispatch and method handlers
└── Makefile
```

### Key Technical Decisions

1. **Protocol**: Line-delimited JSON envelopes over stdio. Commands have `{type, method, request_id, data}`, responses have `{type, status, request_id, error_code?, data}`.

2. **Storage**: SQLite with WAL mode. Two tables: `sessions` (metadata, params, state) and `conversation_turns` (ordered prompt/response history). Chosen for simplicity and zero external dependencies.

3. **Agent invocation**: Shells out to `claude` CLI with `--output-format json --session-id <id> -p <prompt>`. Parses the JSON output to extract the text response. Processes are tracked for stop/kill support.

4. **MI6 integration**: Spawns `mi6-client` as a subprocess, piping stdio through it. Moneypenny auto-generates an ECDSA key on first MI6 use, stores it in `~/.config/james/moneypenny/`. Use `--show-public-key` to get the key for adding to mi6-server's authorized_keys.

5. **Session states**: `idle` (ready for commands) and `working` (agent running). `stop_session` kills the agent and returns to idle. `continue_session` rejected unless idle.

6. **Error handling**: Standardized error codes (SESSION_NOT_FOUND, SESSION_ALREADY_EXISTS, etc.) returned in the response envelope's `error_code` field.

### Methods

- `create_session` - Create new session, run initial prompt, store result
- `continue_session` - Send new prompt to existing idle session
- `list_sessions` - List all sessions with status
- `get_session` - Full session detail with conversation history
- `update_session` - Update session parameters (name, system_prompt, yolo, path)
- `delete_session` - Kill agent if running, remove session
- `stop_session` - Kill running agent, set session back to idle
- `queue_prompt` - Queue a prompt for a working session (auto-drained on completion)
- `import_session` - Create session with pre-existing conversation (no agent run)
- `git_diff` - Run git diff in session's working directory, return output
- `execute_command` - Run arbitrary shell command on the host (`sh -c`), return output + exit code
- `get_version` - Return the moneypenny version

## Hem - Agent Orchestration CLI

Hem is the top-level CLI that manages moneypenny instances and orchestrates sessions across them. kubectl-style verb+noun commands.

### Architecture: Client/Server

Hem uses a client/server architecture over a Unix domain socket (`~/.config/james/hem/hem.sock`).

- **Server** (`hem server`): Long-running daemon that owns SQLite, moneypenny transport connections, and all orchestration logic. Accepts line-delimited JSON requests on the Unix socket. Each connection handles one request/response.
- **CLI** (all other commands): Thin client that parses verb+noun, sends a JSON request to the server, receives a structured JSON response, and formats it for display.
- Commands return structured data (not formatted text). The CLI handles output formatting (`-o json/text/table/tsv`).

### Project Structure

```
hem/
├── go.mod
├── cmd/hem/main.go          # Entry point: thin CLI client + server startup + chat REPL
├── pkg/
│   ├── cli/                 # Verb+noun command parser, plural/alias normalization
│   ├── protocol/            # Shared types: Request, Response (used by both client and server)
│   ├── server/              # Unix socket server, connection handling
│   ├── hemclient/           # Thin client that connects to the server socket
│   ├── store/               # SQLite (moneypenny registry, session tracking, projects)
│   ├── transport/           # FIFO and MI6 client for talking to moneypennies
│   ├── commands/            # All command implementations (return structured data)
│   ├── output/              # Output formatting (json, text, table, tsv)
│   └── ui/                  # TUI (bubbletea + lipgloss)
│       ├── ui.go            # Top-level model, view routing, key bindings
│       ├── client.go        # Server communication wrapper
│       ├── styles.go        # Colors and style definitions
│       ├── dashboard.go     # Attention-based session dashboard
│       ├── sessions.go      # Full session list view
│       ├── projects.go      # Project list + create project form
│       ├── chat.go          # Conversation view with markdown rendering
│       ├── create.go        # Create session form
│       ├── edit.go          # Edit session form
│       ├── diff.go          # Git diff viewer
│       ├── importform.go    # Import session form
│       ├── shell.go         # Remote shell (execute_command)
│       └── moneypennies.go  # Moneypenny management view
└── Makefile
```

### Key Technical Decisions

1. **Client/server split**: Server maintains persistent state and connections. CLI is stateless. Future clients (UI, web) connect to the same socket.

2. **Internal protocol**: Line-delimited JSON over Unix socket. Request: `{"verb":"create","noun":"session","args":[...]}`. Response: `{"status":"ok","data":{...}}` or `{"status":"error","message":"..."}`. One request/response per connection.

3. **CLI pattern**: verb+noun (e.g., `hem add moneypenny`, `hem create session`). Nouns accept singular and plural. Custom parser, no external CLI framework.

4. **Transport**: Server talks to moneypenny via FIFO (local named pipes) or MI6 (spawns mi6-client subprocess). Same JSON envelope protocol.

5. **Storage**: SQLite tracks moneypenny registry, session-to-moneypenny mapping, projects, and local session status (hem_status). Moneypenny owns session data; hem just knows where each session lives and its local completion state.

6. **Output formats**: `--output-type` / `-o` supports json, text, table, tsv. Default is text. Formatting is done by the CLI, not the server.

7. **SSH keys**: Auto-generates ECDSA key for MI6 transport, same approach as moneypenny. `hem show-public-key` to export.

8. **Projects**: Provide organizational context for sessions. A project has defaults (moneypenny, agent, path, system prompt) that sessions inherit when created with `--project`. Status: active/paused/done for filtering.

9. **Dashboard**: Attention-based view grouping sessions by state: REVIEW (idle, needs user attention), WORKING (agent running), COMPLETED (user marked done). Available as both CLI command and TUI default view.

10. **Async agent execution**: Moneypenny runs agents asynchronously — `create_session` and `continue_session` return immediately with session_id, agent runs in background goroutine. Hem polls moneypenny for completion when not using `--async`. This allows moneypenny to handle multiple concurrent requests.

11. **Session lifecycle**: Hem tracks a local `hem_status` (active/completed) separate from moneypenny's status (idle/working). Completing a session hides it from default views. Continuing a completed session automatically reactivates it.

12. **TUI**: Built with bubbletea + lipgloss. View-based architecture: `ui.go` is the top-level router, each view is its own model in a separate file (dashboard.go, sessions.go, chat.go, diff.go, etc). Messages bubble up to the top-level `Update()` which routes to the appropriate view. All views communicate with the hem server via the same Unix socket as the CLI.

13. **Markdown rendering**: Assistant messages in TUI chat use `glamour` with `WithStylePath("dark")`. Must NOT use `WithAutoStyle()` as it sends OSC terminal queries that conflict with bubbletea's terminal control and break the TUI.

14. **Session import**: Supports both JSONL file paths and bare session IDs. For session IDs, walks `~/.claude/projects/` looking for `{id}.jsonl`. Parses Claude Code JSONL format: user messages as string content, assistant messages as text blocks from content arrays.

15. **Resilient deletion**: Session deletion proceeds with local tracking cleanup even when moneypenny is unreachable, reporting a warning rather than failing entirely. This handles stale/orphaned sessions.

16. **Prompt queuing**: When `continue_session` is sent to a working session, hem automatically falls back to `queue_prompt`. Moneypenny stores queued prompts and drains them after agent completion. Each queued prompt is stored as its own conversation turn, but they're joined for the agent. TUI shows queued messages optimistically with ⏳ and `[Queued]` labels.

17. **Dashboard parallelism**: Dashboard queries moneypennies in parallel (one `list_sessions` per moneypenny, not per session) with a 5-second timeout. If a moneypenny is offline, its sessions show as "offline" without blocking other results.

18. **Reviewed/Ready state**: Hem tracks a `reviewed` flag per session. Sessions become unreviewed on `continue_session`, and reviewed when the user views conversation history AND the last turn is from the assistant. This ensures chat polling doesn't prematurely mark sessions as reviewed while agents are still working.

19. **Remote command execution**: `execute_command` runs shell commands on moneypenny hosts via `sh -c`. Exposed in hem as `hem run` and in the TUI as a shell view (`x` key). Shell view can be opened from any session/moneypenny context, inheriting the moneypenny and working directory.

20. **Version display**: All components log their version on startup. `hem --version` shows both client and server versions. TUI shows the version in the status bar.

## Versioning

Single `VERSION` file at project root. Injected at compile time via `-ldflags "-X main.Version=..."`. All components (mi6, moneypenny, hem) share the same version. Semver format.
