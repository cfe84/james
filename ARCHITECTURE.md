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
│   ├── store/                  # SQLite persistence (sessions, conversation history, schedules)
│   ├── agent/                  # Agent subprocess runner (claude CLI invocation)
│   └── handler/                # Command dispatch and method handlers
└── Makefile
```

### Key Technical Decisions

1. **Protocol**: Line-delimited JSON envelopes over stdio. Commands have `{type, method, request_id, data}`, responses have `{type, status, request_id, error_code?, data}`.

2. **Storage**: SQLite with WAL mode. Two tables: `sessions` (metadata, params, state) and `conversation_turns` (ordered prompt/response history). Chosen for simplicity and zero external dependencies.

3. **Agent invocation**: Supports multiple agent types. Claude: shells out to `claude` CLI with `--output-format json --session-id <id> -p <prompt>`, parses JSON output. GitHub Copilot: shells out to `copilot` CLI with `--resume <id> -s -p <prompt>`, uses `--yolo` for permissions, parses plain text output. Agent type is dispatched via `buildArgs()` and `parseOutput()` functions. Processes are tracked for stop/kill support.

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
- `git_commit` - Stage all changes (git add -A) and commit with a message
- `git_branch` - Create and switch to a new branch (git checkout -b)
- `git_push` - Push current branch to origin with -u
- `execute_command` - Run arbitrary shell command on the host (`sh -c`), return output + exit code
- `list_directory` - List directory entries (name + is_dir) at a given path, skipping hidden files
- `get_version` - Return the moneypenny version
- `schedule` - Schedule a future continuation for a session (prompt + time)
- `list_schedules` - List all schedules for a session (pending, fired, cancelled)
- `cancel_schedule` - Cancel a pending schedule by ID

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
├── assets/
│   ├── embed.go             # go:embed for notification.wav
│   └── notification.wav     # Notification sound (embedded at build time)
├── cmd/hem/main.go          # Entry point: thin CLI client + server startup + chat REPL
├── pkg/
│   ├── cli/                 # Verb+noun command parser, plural/alias normalization
│   ├── protocol/            # Shared types: Request, Response (used by both client and server)
│   ├── server/              # Unix socket server, connection handling
│   ├── hemclient/           # Thin client that connects to the server socket
│   ├── store/               # SQLite (moneypenny registry, session tracking, projects)
│   ├── transport/           # FIFO and MI6 client for talking to moneypennies
│   ├── commands/            # All command implementations (return structured data)
│   │   ├── commands.go      # Core dispatch, session/project/moneypenny commands
│   │   └── notification.go  # Sound notification (embedded wav, afplay/aplay)
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
│       ├── wizard.go        # 3-step create session wizard (mp, path, form)
│       └── moneypennies.go  # Moneypenny management view
└── Makefile
```

### Key Technical Decisions

1. **Client/server split**: Server maintains persistent state and connections. CLI is stateless. Clients connect via Unix socket or MI6 transport (using the `Sender` interface: `SocketSender` for local, `MI6Sender` for remote). The server can also accept commands from an MI6 control channel (`--mi6-control` flag, implemented in `server/mi6.go`). The default connection can be persisted with `hem set-default server --hem ADDR` (MI6) or `--local` (Unix socket). The `--local` global flag overrides any stored default.

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

13. **Markdown rendering**: Both assistant and user messages in TUI chat use `glamour` with `WithStylePath("dark")`. Must NOT use `WithAutoStyle()` as it sends OSC terminal queries that conflict with bubbletea's terminal control and break the TUI.

14. **Session import**: Supports both JSONL file paths and bare session IDs. For session IDs, walks `~/.claude/projects/` looking for `{id}.jsonl`. Parses Claude Code JSONL format: user messages as string content, assistant messages as text blocks from content arrays.

15. **Resilient deletion**: Session deletion proceeds with local tracking cleanup even when moneypenny is unreachable, reporting a warning rather than failing entirely. This handles stale/orphaned sessions.

16. **Prompt queuing**: When `continue_session` is sent to a working session, hem automatically falls back to `queue_prompt`. Moneypenny stores queued prompts and drains them after agent completion. Each queued prompt is stored as its own conversation turn, but they're joined for the agent. TUI shows queued messages optimistically with ⏳ and `[Queued]` labels.

17. **Dashboard parallelism**: Dashboard queries moneypennies in parallel (one `list_sessions` per moneypenny, not per session) with a 5-second timeout. If a moneypenny is offline, its sessions show as "offline" without blocking other results.

18. **Reviewed/Ready state**: Hem tracks a `reviewed` flag per session. Sessions become unreviewed on `continue_session`, and reviewed when the user views conversation history AND the last turn is from the assistant. This ensures chat polling doesn't prematurely mark sessions as reviewed while agents are still working.

19. **Remote command execution**: `execute_command` runs shell commands on moneypenny hosts via `sh -c`. Exposed in hem as `hem run` and in the TUI as a shell view (`x` key). Shell view can be opened from any session/moneypenny context, inheriting the moneypenny and working directory.

20. **Version display**: All components log their version on startup. `hem --version` shows both client and server versions. TUI shows the version in the status bar.

21. **Client-side notifications**: Notification sounds are played client-side when a session transitions from WORKING to READY. The TUI detects these transitions during dashboard auto-refresh polling and plays the embedded WAV file via `afplay` (macOS) or `aplay` (Linux). The WAV is embedded at build time from `hem/assets/notification.wav` using `go:embed` and cached at `~/.config/james/hem/notification.wav`. The `--silent` flag on `hem ui` disables sound. Qew detects the same transitions during its dashboard polling and plays a Web Audio API chime, plus shows a slide-in pop-over notification. Qew has a header toggle button (bell icon) to enable/disable sound.

22. **Create session wizard**: TUI session creation uses a 3-step wizard (`wizard.go`): (1) select moneypenny from a list, (2) browse remote filesystem to pick a working directory via `list_directory` moneypenny method, (3) fill in prompt and options. Esc navigates back through steps. The wizard replaces the old single-screen create form for all TUI entry points (dashboard, project detail, session list).

23. **Scheduled continuation**: Moneypenny supports time-delayed session continuations. Schedules are stored in a `schedules` SQLite table (`id`, `session_id`, `prompt`, `scheduled_at`, `status`, `created_at`, `cron_expr`). A scheduler goroutine ticks every 30 seconds and also runs on boot to catch any schedules that came due while offline. When a schedule fires: if the session is idle, the prompt is sent as a direct `continue_session`; if the session is busy, the prompt is queued via the existing `queue_prompt` mechanism (same as TUI message queuing). Recurring schedules are supported via `--cron` with standard 5-field cron expressions (minute hour dom month dow, numbers and `*`) and shorthands (`@hourly`, `@daily`, `@every 2h`); when a recurring schedule fires, a new occurrence is automatically created for the next matching time. When any schedule fires (one-shot or recurring), a "system" conversation turn is added to the chat, recording when the task was triggered; the TUI renders these system turns with a gear icon in muted/italic style. Agents can self-schedule by emitting `<schedule at="...">prompt</schedule>` tags in their output, which moneypenny parses from agent responses. Schedule instructions are appended to every session's system prompt via a `scheduleSystemPromptSuffix` constant so agents know the capability exists. Time values accept RFC3339, relative formats (`+2h`, `+30m`), and local time strings. In the TUI, schedules are displayed in chat view with a clock icon, and the `t` key in command mode opens schedule management.

24. **Git operations**: Moneypenny exposes `git_commit`, `git_branch`, and `git_push` methods alongside the existing `git_diff`. These run git commands in a session's working directory: `git_commit` stages all changes (`git add -A`) and commits; `git_branch` creates and checks out a new branch; `git_push` pushes the current branch to origin with `-u`. Hem exposes these as `hem commit session`, `hem branch session`, and `hem push session`.

25. **Dashboard auto-refresh**: The TUI dashboard polls moneypennies every 5 seconds in the background, regardless of which view is active. When a session transitions from WORKING to READY during a poll, a notification sound is played client-side. Both the TUI and Qew track session states independently and detect transitions locally.

26. **Queued message visual state**: The TUI preserves the "Queued" indicator (with its icon and label) on user messages across poll refreshes. The queued state is only cleared when an assistant response appears in the conversation, preventing the visual indicator from flickering or disappearing during polling cycles.

27. **Session sync**: Hem server periodically syncs sessions from all registered moneypennies (on startup + every 5 minutes). Queries `list_sessions` on each moneypenny and uses `INSERT OR IGNORE` to adopt unknown sessions without overwriting existing tracking data. This allows multiple hem instances to share moneypennies and discover each other's sessions.

## Qew - Web UI for Remote Access

Qew is a web-based UI that connects to a Hem server via MI6, enabling remote access from phones and other computers.

### Project Structure

```
qew/
├── go.mod
├── cmd/qew/main.go         # Entry point: SSH key gen, MI6 connect, HTTP server
└── pkg/web/
    ├── server.go            # HTTP server (API proxy, WebSocket, embedded static files)
    ├── mi6.go               # MI6 client transport (persistent connection, auto-reconnect)
    └── static/              # Embedded web frontend (HTML, CSS, JS)
        ├── index.html       # Dashboard and chat UI (dark theme)
        └── app.js           # Frontend logic (dashboard polling, chat, API calls)
```

### Key Technical Decisions

1. **MI6 transport**: Connects to Hem server via MI6 control channel, using the same JSON request/response protocol as the Unix socket. Auto-reconnects with backoff on connection loss.

2. **SSH key management**: Auto-generates ECDSA key on first run (`~/.config/james/qew/qew_ecdsa`). Use `--show-public-key` to export for MI6 authorized_keys.

3. **API proxy**: HTTP `POST /api` proxies JSON requests to Hem via MI6. WebSocket at `/ws` provides real-time updates.

4. **Static embedding**: Web frontend is embedded at build time via `embed.FS`, making Qew a single binary with no external dependencies.

5. **Polling**: Dashboard polls every 5s, chat polls every 3s, matching the TUI behavior.

6. **Chat features**: Chat view fetches session status and schedules alongside history. Shows "working..." indicator when session is active, queued message labels for optimistic sends, and pending schedule times.

7. **Client-side notifications**: Dashboard polling tracks session states and detects WORKING→READY transitions. Plays a Web Audio API chime and shows a slide-in pop-over notification. Sound can be toggled via a header button.

## Docker Deployment

A combined `Dockerfile` at the project root builds both Hem and Qew into a single `james` image. The entrypoint starts Hem server in the background, waits for its Unix socket, then starts Qew in the foreground connected via that socket.

### Build
- Two builder stages: `hem-builder` (CGO_ENABLED=1, needs `gcc`/`musl-dev` for SQLite) and `qew-builder` (CGO_ENABLED=0).
- Final image is `alpine:3.20` with both binaries.
- Pipeline: `.build/james.ini` triggers on `hem/`, `qew/`, or `VERSION` changes on main.
- `.build/james-build.sh` — builds the `james` Docker image.

### Runtime
- **Entrypoint** (`docker/entrypoint.sh`): Prints both Hem and Qew SSH public keys (for MI6 `authorized_keys`), starts `hem server` in background, then `qew` in foreground.
- **Env vars**: `HEM_MI6_URL` (MI6 address for Hem control channel), `QEW_PASSWORD` (Qew web auth, omit for `--development` mode), `LISTEN` (default `:8077`).
- **Volume**: `JAMES_CONFIG_PATH` mounted to `/root/.config/james` (persists SQLite DB, SSH keys).
- **Port**: 8077 (configurable via `QEW_PORT` env var in deploy script).

### Deploy
- `.build/james-deploy.sh` — requires `HEM_MI6_URL` and `JAMES_CONFIG_PATH`. Optional: `QEW_PASSWORD`, `QEW_PORT`. Stops existing container, creates new one.

## Versioning

Single `VERSION` file at project root. Injected at compile time via `-ldflags "-X main.Version=..."`. All components (mi6, moneypenny, hem, qew) share the same version. Semver format.
