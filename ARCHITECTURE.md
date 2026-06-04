# James Architecture

## MI6 - Agent Communication Relay

MI6 is a transport layer for remote agents to communicate via sessions. It consists of two binaries: `mi6-client` (local) and `mi6-server` (remote/container).

### Project Structure

```
mi6/
├── go.mod
├── cmd/
│   ├── mi6-client/main.go
│   └── mi6-server/
│       ├── main.go        # Server entry point, connection handling
│       └── admin.go       # Admin session handler, key management
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

5. **Compression**: Per-message gzip, negotiated during handshake via capability flags in MsgHello/MsgServerHello payloads (byte 33+). Both sides advertise capabilities; intersection is used. When enabled, plaintext is prefixed with a 1-byte flag (`0x00`=raw, `0x01`=gzip) before encryption. Messages under 128 bytes skip compression. Falls back to raw if gzip doesn't reduce size. Backward compatible: old clients send 32-byte hello payloads (no capabilities), new peers detect this and disable compression.

6. **Sessions**: Lazy creation (first client join creates session). In-memory only (no persistence). Server broadcasts data to all OTHER connected clients in the same session.

7. **Batching**: Client batches stdin with triple trigger: newline, buffer size (4KB), or idle timeout (100ms).

8. **authorized_keys**: Standard OpenSSH format, reloaded on SIGHUP.

9. **Admin key management**: A separate `admin_keys` file (same OpenSSH format, same directory) grants admin access. Admin clients join the reserved `__admin__` session, which the server intercepts before the normal session join flow. Admin commands (list/add/delete authorized keys) are JSON over MsgData. File writes use atomic temp-file-rename to prevent corruption. After modifications, authorized_keys are reloaded in-process (same as SIGHUP). The mi6-client `--admin-command` flag provides single-shot admin request/response mode. Hem wraps this as `list/add/delete mi6-key` commands.

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

3. **Agent invocation**: Supports multiple agent types. Claude: shells out to `claude` CLI with `--output-format json --session-id <id> -p <prompt>`, parses JSON output. GitHub Copilot: shells out to `copilot` CLI with `--resume <id> -s` and the prompt piped via **stdin** (no `-p`), uses `--yolo` for permissions, parses JSON stream output. Agent type is dispatched via `buildArgs()` and `parseOutput()` functions. Processes are tracked for stop/kill support. Copilot prompts go through stdin rather than an inline `-p` value because on Windows, npm installs the CLI as a `.cmd`/`.ps1` batch shim that Go runs through `cmd.exe`, which truncates the command line at the first newline — silently dropping everything after the first line of a multi-line `-p` prompt (observed as agents receiving only the first line of review comments). Copilot reads its prompt from a non-TTY stdin when `-p` is omitted, so piping sidesteps argv entirely (also avoids the Windows ~32KB argv limit). The `@file` form is unusable — copilot treats `@` as an attachment, not prompt text.

4. **MI6 integration**: Spawns `mi6-client` as a subprocess, piping stdio through it. Moneypenny auto-generates an ECDSA key on first MI6 use, stores it in `~/.config/james/moneypenny/`. Use `--show-public-key` to get the key for adding to mi6-server's authorized_keys.

5. **Session states**: `idle` (ready for commands) and `working` (agent running). `stop_session` kills the agent and returns to idle. `continue_session` rejected unless idle.

6. **Error handling**: Standardized error codes (SESSION_NOT_FOUND, SESSION_ALREADY_EXISTS, etc.) returned in the response envelope's `error_code` field.

### Methods

- `create_session` - Create new session, run initial prompt, store result
- `continue_session` - Send new prompt to existing idle session
- `list_sessions` - List all sessions with status and agent
- `get_session` - Full session detail with conversation history
- `update_session` - Update session parameters (name, system_prompt, yolo, path)
- `delete_session` - Kill agent if running, remove session
- `stop_session` - Kill running agent, set session back to idle
- `queue_prompt` - Queue a prompt for a working session (auto-drained on completion)
- `import_session` - Create session with pre-existing conversation (no agent run)
- `summarize_session` - Invoke the session's agent as a one-shot over the full transcript and return a standalone summary. Reuses `CompactSession` (also used by the agent-side recovery path when the upstream session is lost). Exposed to hem so users can drive compaction explicitly via `hem summarize session` and bootstrap copies via `hem copy session`.
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

17. **Dashboard async cache with broadcast refresh**: Dashboard and ListSessions use a shared moneypenny session cache on the Executor. On each call, cached data is returned instantly while a background goroutine refreshes the cache by querying all moneypennies in parallel (10s timeout). The first-ever call blocks synchronously (up to 3s) to populate the cache. Mutations (create, continue, stop, delete) invalidate the relevant moneypenny's cache entry and trigger an immediate background refresh. As each moneypenny responds during a background refresh, a broadcast message (`verb: "refresh", noun: "dashboard"`) is pushed through the MI6 control channel. The TUI's broadcast listener picks these up and triggers an incremental dashboard reload, so fast moneypennies update the display immediately without waiting for slow/offline ones.

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

28. **Sub-agents**: Sessions can spawn sub-sessions for parallel task execution. Sub-sessions use the same moneypenny session model, linked by a `parent_session_id` column in hem's SQLite. Moneypenny injects the `HEM_SESSION_ID` environment variable into the agent runner, enabling agents to call `hem create subsession` and `hem watch session` from within their process. The `watch session` command uses an in-memory polling loop (not persistent watchers) that checks sub-agent status and queues completed results back to the parent via `queue_prompt`. Dashboard and session listing filter out sub-sessions (`parent_session_id != ''`). Deleting a parent cascades to all sub-sessions. Sub-agents are displayed in TUI and Qew chat views. The gadgets system prompt includes sub-agent instructions.

29. **Real-time agent activity streaming**: Moneypenny streams agent activity for Claude sessions using `--output-format stream-json` instead of the blocking `--output-format json`. The `runStreaming()` function pipes the agent's stdout, scans line by line, and parses each JSON event in real time. This is Claude-only; Copilot agents still use blocking `cmd.Output()` since they lack streaming support. Each session maintains an in-memory ring buffer of 30 events, protected by a mutex, with no persistence to disk. Activity events carry a type (`thinking`, `tool_use`, `text`), a human-readable summary, and a timestamp. The `toolSummary()` function extracts readable descriptions from `tool_use` blocks (e.g., file paths from read/write operations, commands from shell invocations). TUI and Qew poll the `get_session_activity` endpoint when a session is in the "working" state. Events are cleared when the agent finishes, so the UI only shows activity while work is in progress.

30. **Model selection**: TUI forms (wizard, edit) use cycling selectors for agent and model fields instead of free-text input. Models are discovered per moneypenny via the `list_models` method: Claude returns hardcoded aliases (sonnet, opus, haiku); Copilot shells out to `copilot -p` asking it to list available model identifiers (one per line), letting copilot use its **default** model — no `--model` is pinned, because model identifiers are retired over time (e.g. `gpt-4.1`) and a stale pin makes the query fail outright and surface as "no models". The wizard caches models per agent type and reloads when the agent selection changes. The edit form loads models when the session detail arrives (using the session's agent type). Model options always include an empty value (no override / default). If the session already has a model not in the discovered list, it's added as an option to preserve it. **Qew mirrors this (v1.8.0):** the create wizard exposes Agent (dropdown defaulting to copilot), Model and Effort `<select>`s; changing the agent calls `syncWizardAgentDeps()` which rebuilds the effort options (`effortOptions()` mirrors hem's per-agent list) and re-fetches models via the `list-models` verb (a stale-fetch guard ignores a response if the agent changed again meanwhile). The edit-session dialog adds Model and Effort dropdowns (agent is fixed for an existing session); models are fetched from the session's `moneypenny`+`agent`, the current model is preserved as a `(current)` option when absent from the list, and clearing effort emits the backend's `none` clear sentinel. Shared JS helpers (`loadModels`, `modelOptionsHtml`, `effortOptionsHtml`) keep both dialogs consistent. The git diff review modal opens via a `modal-large` variant class (97vw/97vh, flex column) so large diffs are readable; `renderWizardModal(content, modalClass)` takes an optional class and the diff body (`.diff-content`) flexes to fill the height and **wraps long lines** (`white-space: pre-wrap; overflow-wrap: anywhere`) so no horizontal scrolling is needed. The Git Log modal shares the same `modal-large` variant. **Git log commit review (v1.10.0):** `formatGitLog` wraps each commit line (matched by the graph+hash regex) in a clickable `.log-commit` span carrying `data-hash`; a click delegate calls `showCommit(hash)`, which fetches `git-show session <id> --hash <hash>` (`git show --stat --patch`) and renders it through the *same* review machinery as the working-tree diff. The shared `diffReview` state gained a `mode` (`'diff'`|`'commit'`) and `commit` (hash) field: `renderDiffView()` swaps the title (Commit `<short>`) and actions (Back instead of Commit/Commit&Push) and `buildReviewPrompt()` swaps the boilerplate to reference the commit hash. `parseDiffLines(text, {commitPreamble:true})` leaves the commit-metadata/diffstat preamble (everything before the first `diff ` header) non-commentable since it has no file/line context. Stale-response races (log→commit→back→another commit) are guarded by a monotonic `gitViewToken` checked after every await; **Back** confirms before discarding unsent comments. This commit-review capability is Qew-only — the TUI's `diffTabCommit` shows commit contents read-only. The diff is parsed client-side by `parseDiffLines()` (a JS port of hem's `parseDiffMeta`) into per-line metadata (file, real line number, code), rendered as clickable `.diff-line` blocks; clicking opens an inline comment editor and saved comments live in per-line slots keyed by the line's sequential index. `buildReviewPrompt()`/`formatReviewComment()` are JS ports of the same functions in `hem/pkg/ui/diff.go`, producing a byte-identical review prompt that is sent to the agent via the normal `continue session --async` path (so TUI and Qew reviews are indistinguishable to the agent). The Qew header shows the running version: `main.Version` (build-time `-ldflags`) is passed into `web.NewServer` and served from an unauthenticated `/version` endpoint that `app.js` fetches on load.

31. **Session memory**: Each session has a persistent `memory` text column in moneypenny's SQLite. Memory is injected into the system prompt at runtime (not stored in the system prompt field) via `<session-memory>` tags in the `runAgent()` function, so agents always see the latest content. Memory instructions are appended to the system prompt at session creation time whenever hem has MI6 connectivity, independently of the gadgets flag. The instructions tell agents to use `hem update memory SESSION_ID CONTENT` (replace semantics, not append). Hem exposes `show memory` and `update memory` commands. TUI provides a memory editor view (`m` key in chat command mode) with Ctrl+S to save. The editor supports vertical cursor navigation for large memories (↑/↓ by line, Ctrl+U/Ctrl+D or PgUp/PgDn by half a page) via line-aware movement primitives on the shared `textInput` component, with the viewport scrolling to keep the cursor visible. When toggling gadgets on/off, the memory marker is preserved independently of the gadgets marker.

32. **Diagnostics**: `hem diagnose` uses a two-phase client-side architecture for streaming output. Phase 1 runs local checks (data directory, SSH keys, database) without a server connection, printing results immediately. Phase 2 sends a single `diagnose` command to the server, which pings all moneypennies in parallel, checks agent availability via `check_agents`, and collects cache/session stats. The CLI unpacks the structured `DiagnoseResult` and prints each section as it goes. JSON mode (`-o json`) buffers all checks and outputs a single JSON array at the end. Agent binary detection uses `exec.LookPath()` for cross-platform support (Windows, macOS, Linux). The `check_agents` moneypenny command is version-gated (≥1.0.0) for retrocompatibility with older moneypenny instances.

33. **Session summarization and copying**: `hem summarize session` exposes the existing `CompactSession` helper to users so they can produce a standalone summary of a session's history on demand (originally written for the agent-side recovery path when an upstream agent's session is lost). `hem copy session SOURCE_ID` builds on top: it fetches the source session detail via `get_session`, asks the source moneypenny for a summary via `summarize_session`, then runs the regular `create_session` path on the target moneypenny (which can differ from the source). Parameters default to the source's values and are overridden by any explicit flag, including `-m` for cross-host copies. The summary is wrapped in `<prior-session-summary>` tags inside a preamble that explains the new session is a continuation; trailing args (if any) are appended as the actual user follow-up, otherwise a stub "acknowledge and await further instructions" is inserted. Source session is preserved. The TUI surfaces this via `y` (open the create wizard prefilled from the selected session; submit triggers `copySession` instead of `createSession`) and `S` (open the summary view, which renders the summary and offers a save-to-file modal prefilled with `<cwd>/<session-name>-summary.md`). Qew mirrors the TUI's copy flow with a **Duplicate Session** action in the chat Actions menu: it reuses the create wizard in copy mode, prefilling from `show session` and submitting via `copy session`. Because copy inherits source values per-field, Qew omits `--system-prompt` unless the user edits the prefilled prompt (so the backend's marker stripping still runs), emits an explicit `--yolo=true|false` (the only way to override a yolo source given the backend's `yoloExplicit` prescan), and forwards any source trait IDs that have no checkbox (e.g. since-deleted definitions) so they aren't silently dropped. `summarize_session` returns a `turn_count` (`CompactSession` now returns `(summary, turnCount, error)`) so callers never conflate "no history yet" with "the summarizer agent returned an empty string despite stored turns": when `turn_count > 0` and the summary is empty, `hem copy session` aborts and `hem summarize session` errors out (instead of fabricating a `(no history)` preamble/result), since an empty summary over a non-empty transcript indicates a transient summarizer failure (e.g. the session's stored model was later retired by the agent CLI). The literal `(the source session had no conversation history yet)` fallback is reserved for genuinely empty sources (`turn_count == 0`).

34. **Persistent model cache**: `list_models` against copilot shells out to `copilot -p` which takes 10-20s. Moneypenny memoizes the result in-process (24h TTL) but loses it on every restart. Hem persists the same result in its SQLite (`model_cache` table, keyed by moneypenny+agent). `hem list-models` is now cache-first: a cache hit returns immediately, a cache miss queries the moneypenny synchronously and writes the result. Every session-creation verb (`create session`, `create subsession`, `copy session`) fires `asyncRefreshModelCache(mp, agent)` after the moneypenny accepts the create, so the cache stays warm without paying latency on the user's path. ContinueSession deliberately doesn't warm — the session row doesn't carry the agent, and continuing isn't a wizard entry point.

    Hammering protection is layered: a 60s freshness floor skips refreshes when the cache row is younger than a minute, AND a `sync.Map` of in-flight refreshes ensures only one goroutine per `(mp, agent)` runs at a time. The floor alone would be TOCTOU under burst creates (multiple goroutines pass the check before any writes); the in-flight map closes that race.

    Empty-response handling is split by caller: background warmup and cache-miss paths treat empty as transient (preserve any existing good row), while `--refresh` and `refresh-models` write through (lets users recover from a permanently revoked source — without this, a stale row would outlive the access). `RefreshModels` returns a short confirmation instead of the model list to keep the verb's output focused.

    Cache rows are dropped when a moneypenny is deleted so re-adding the same name doesn't surface a stale list. The TUI's wizard automatically benefits — `listModels` goes through this cache-first path with no client-side change.

35. **Agent traits**: Traits are reusable, hem-level system-prompt snippets (`id`, `name`, `prompt`) toggled on/off per session. They live entirely in hem (moneypenny is unaware), persisted in two SQLite tables: `traits` (definition) and `session_traits` (the `session_id`↔`trait_id` selection mapping, cleared on trait delete). Trait CRUD mirrors projects (`CreateTrait`, `GetTrait` by id-then-name, `ListTraits`, `UpdateTrait` with nil-pointer = skip, `DeleteTrait`, plus `SetSessionTraits`/`GetSessionTraits`).

    Composition is **compose-at-write**: the selected traits' prompts are rendered into the session's system prompt at create/update time and sent to the moneypenny, exactly like gadgets. The canonical system-prompt order is **base → traits → gadgets → memory**. Because trait content is arbitrary user text (unlike the fixed gadgets/memory markers), the traits block is wrapped in `<!--james:traits:begin-->`/`<!--james:traits:end-->` sentinel markers; `stripTraitsBlock`/`insertTraitsBlock` strip and reinsert it while preserving the gadgets/memory blocks. `resolveTraits` parses the comma-separated `--traits` spec (IDs or names), de-duplicates preserving order, and errors on unknown traits.

    `CreateSession`/`CopySession` append the traits block before gadgets/memory and persist the mapping via `SetSessionTraits`; `CopySession` inherits the source's traits unless `--traits` is given (and strips the inherited traits block to avoid double injection). `UpdateSession` detects an explicit `--traits` (pre-scanning args, like `yoloExplicit`, so an empty value can clear all traits), strips the old block, inserts the new one, persists the mapping, and re-sends the recomposed prompt — so subsequent chat turns use the new traits. A lazily-cached `getCurrentSP()` closure is shared by the traits and gadgets recomposition paths so both operate on the same evolving prompt within a single update. Editing a trait *definition* does NOT retroactively rewrite existing sessions (only a session's selection change recomposes that session). `ShowSession` strips the traits block from the displayed prompt and returns the selected IDs from `session_traits` (not parsed from the prompt).

    TUI: a dedicated traits management view (`traitsModel` + `editTraitModel` in `traits.go`, dashboard key `t`) lists traits with new/edit/delete. Trait selection in the create wizard and edit-session form reuses the existing bool `formField` (rendered as `[x]`/`[ ]`) with a new `traitID` field; trait fields are loaded asynchronously and appended to the form, and the serialization loops collect checked trait IDs into `--traits`. The edit form extends `original[]` so change-detection works and emits `--traits` (possibly empty) only when the selection changed. Qew exposes the same: a **Traits** nav button with a management view, plus trait checkboxes in the create wizard and edit-session dialogs (the generic verb/noun API needs no new endpoints).

    **Default traits & UI polish (v1.7.0):** traits carry an `enabled_by_default` boolean (column added to `traits`, migrated via the standard idempotent `ALTER TABLE … ADD COLUMN` loop; `--default=true|false` flag on `create/update trait`, surfaced as a "Default" column in `list trait` and an "Enable by default" toggle in the editors). The `--default=value` single-token form is used deliberately because Go's `flag.BoolVar` cannot consume a space-separated boolean value (`--default false` would parse as `--default=true` plus a positional `false`); `UpdateTrait` takes a `*bool` (nil = leave unchanged) with explicit-arg detection so the flag can be toggled off. `CreateSession` applies default-enabled traits **only when `--traits` is entirely absent** (detected by pre-scanning args, mirroring `traitsExplicit`); an explicit `--traits` — even empty — wins, so deselecting all traits in the UI is honoured. To make this robust against the asynchronous trait load, the create wizard / Qew create flow emit an explicit `--traits` **only after traits have loaded** (`traitsLoaded` / `traitsCache.length`); before that they omit the flag so backend defaults still apply. Default traits are pre-checked in the create wizard (create mode only, not copy). Form label columns are now sized dynamically (`formLabelWidth`, clamped to `[16,40]`, with display-width-aware `truncateDisplay`) instead of a fixed `Width(24)`, so long trait names no longer wrap; the `"Trait: "` checkbox prefix was dropped.

36. **Copilot reply assembly**: Conversation turns distinguish the final `assistant` reply from the dim "train of thought" (`thinking`/`agent_text` turns). Claude has a dedicated `result` event for the final answer, so its streamed `text` blocks are persisted as `agent_text` and the handler dedups the trailing duplicate against the `result`. Copilot has **no** result event — the answer is conveyed purely through `assistant.message` events. Copilot tags each `assistant.message` with a **`phase`** field: `commentary` for pre-tool narration ("Now let me look at X", emitted as its own message, often carrying a `toolRequests` array) and `final_answer` for the concluding reply. Accumulating *every* message into the reply made it very chatty (all the preambles leaked into the bubble). `runCopilotStreaming` now **classifies at end-of-stream**, mirroring Claude's split: it buffers each message/reasoning event in order (recording the `phase`), then — **when any message carries a phase** — the reply is exactly the message(s) tagged **`final_answer`** and everything else is train of thought. This is the provider's own ground truth and is robust to tool-call position. For **older Copilot builds that omit `phase`**, it falls back to the prior positional heuristic: the **trailing contiguous run of no-tool messages** (the model talking after it stopped acting), with a **further fallback to the last non-empty message** if there is no such run (so an answer bundled with a housekeeping tool call is never hidden). Empty tool-only messages are recorded solely as boundaries for that fallback and never persisted. Everything else — preamble narration (persisted as `agent_text`) and reasoning (persisted as `thinking`) — is written to the train of thought in original order; the reply itself is **not** persisted there (the handler stores it as the `assistant` turn, so the Claude-only dedup is a no-op for Copilot). All events still stream live via the activity buffer during the turn. The reply is computed before `cmd.Wait` so the error path still carries partial text for diagnostics.

37. **Qew chat scroll-back pagination**: The Qew chat view loads only the latest page of turns on open and lets the user scroll up to fetch older history, porting the TUI's incremental-history model (`hem/pkg/ui/chat.go`) to `qew/pkg/web/static/app.js`. `chatConversation` holds **only** server turns (queued optimistic messages stay in a separate array), ordered older→recent; `chatRecentCount` tracks the size of the latest poll window and `chatTotal` the server total. The 3s poll (`loadChat`) fetches the recent window with `history session --count 50 --from 0` (where `from` is **end-relative**) and folds it in via `mergeRecentHistory`: because new turns shift the end-relative boundary, the count of preserved older turns is `clamp(prevServerKnown - recent.length + delta, 0, prevServerKnown)` with `delta = total - previousTotal`. Two degenerate cases reset to the recent window to keep the array contiguous (the invariant `loadOlderHistory` relies on): a shrinking total, and a burst larger than one page (`delta > recent.length`, which would otherwise leave an unrecoverable gap — the same case the TUI logs as "gap detected" but Qew handles by resetting rather than concatenating). Scrolling to within 60px of the top calls `loadOlderHistory`, which fetches `--from chatConversation.length` and **prepends**, preserving the viewport by offsetting `scrollTop` by the added height. Two mutators (poll + older-fetch) never corrupt the shared array: the poll **skips the merge while `chatLoadingMore`** is set (only updating `chatTotal`), and the older-fetch **discards its response if the session changed or `chatTotal` moved** during the await (the user simply scrolls again to retry). A stale-session guard (`sessAtStart`) in `loadChat` prevents a late poll from overwriting a different session's state. `renderChat(prepend)` reads the merged globals plus cached schedules/subagents/activity (so an older-fetch re-render needn't refetch them), filters queued messages against **only** the recent window (matching the whole loaded history could drop a freshly-queued repeat of old text), and preserves the reading position across polls (restores `prevTop` instead of resetting to 0) unless the user is near the bottom or a `chatForceScrollBottom` one-shot (set on send) forces the bottom. The scroll listener is attached once at init and stays correct across session switches because `openChat` resets all pagination globals.

38. **Qew keyboard navigation**: A single document-level `keydown` listener (attached once at init) gives the Qew SPA TUI-parity keyboard control, dispatching by the active view (detected via the views' inline `display` style and `currentSession`) and bailing out entirely when a `.modal-overlay` is open. On the dashboard, `j`/`ArrowDown` and `k`/`ArrowUp` move a selection highlight and `Enter` opens the selected row (reusing the shared `openDashEntry()` helper that the row click handler also calls, so subagent rows open their parent then `_openSubagent`). Selection is tracked by **session id** (`dashSelectedId`), not list index, so it survives the 5-second dashboard re-render: `renderDashboard` records the rows in display order into `dashEntries` and calls `applyDashSelection()` to re-apply the `.session-row.selected` class after every rebuild; `dashMove` clamps at the list ends (no wrap, mirroring the TUI) and `scrollIntoView({block:'nearest'})` keeps the selection visible. Dashboard keys are ignored while focus is in an `input`/`textarea`/`select`/contentEditable or when a modifier (ctrl/meta/alt) is held, so they don't clobber the project-filter dropdown or browser shortcuts. In a conversation, `Escape` calls `closeChat()` (which pops back to the parent first when viewing a subagent, else returns to the dashboard) and `Ctrl+U`/`Ctrl+D` call `chatScroll(±1)` to scroll `#chat-messages` by half its `clientHeight`; scrolling up near the top naturally reuses the v1.12.0 scroll listener to load older history. The selection highlight uses an inset `outline` (not a `border`) to avoid reflowing the row layout. While a `.modal-overlay` is open the listener stops dispatching view-nav keys, but `Escape` is special-cased to invoke `escapeCloseModal()`, which clicks the modal's own close button (the first `Close`/`Cancel`/`Back`/`OK` `button` in the modal's primary `:scope > .modal-actions` row) so the button's logic still runs (e.g. the diff review's confirm-before-discard); if focus is inside the inline diff comment editor (`.diff-comment-editor`), it instead clicks that editor's Cancel so a stray Escape doesn't blow away the whole review. **TUI-parity extension (v1.15.0):** in a conversation `Escape` no longer calls `closeChat()`; instead it opens an in-chat **command palette** (`openCmdPalette`), a `.modal-overlay` whose inner `.cmd-palette` div is tagged so the keydown handler can route single-key actions to it (`c`/`e`/`y`/`g`/`s`/`d`/`q` → complete/edit/duplicate/diff/stop/delete/back, `Escape` → `closeCmdPalette(true)` which removes the overlay and refocuses `#chat-input`). The palette branch runs *before* the generic modal-Escape branch and ignores ctrl/meta/alt and auto-repeat; `runCmd` tears the palette down (`cmdPaletteOpen=false` + `closeWizard()`) before invoking the action so follow-up modals aren't shadowed. The dashboard branch gained single-key shortcuts: global `m`/`b`/`n`/`p`/`t` (moneypennies / bell-sound toggle / new session / projects / traits) and selection-scoped `c`/`e`/`y`/`d` (complete/edit/duplicate/delete the highlighted row, resolved at action time via `dashAction`, no-op if nothing selected). To support acting on a non-open session, `completeSession`/`deleteSession`/`showEditSessionModal`/`openDuplicateWizard` now take an optional session id (defaulting to `currentSession`), captured at entry; after a complete/delete they call `closeChat()` when the target is the open chat else `loadDashboard()`, and `showEditSessionModal` renders its loading modal *before* the `loadTraitsCache` await (closing a TOCTOU gap) and only rewrites `#chat-title` when editing the open session. `closeWizard()` resets `cmdPaletteOpen` so a background-click dismissal can't strand the flag. Letter shortcuts use a text-field guard (`input`/`textarea`/`select`/contentEditable); the `button`/`a` guard is kept only for `Enter` so toolbar buttons retain native activation. The whole handler early-returns on `isComposing` and treats `repeat` as a guard. Each dashboard row also renders a `.session-agent` badge (`agent-copilot` orange / `agent-claude` violet) from the dashboard `Agent` column (`row[8]`). **Git diff navigation (v1.18.0):** the diff/commit review modal (`#diff-review-content`) has its own keyboard handler (`handleDiffModalKey`, called from the modal-overlay branch after the Escape special-case) that moves a line cursor (`diffReview.cursor`, a sequence index into `diffReview.lines`): `j`/`k`/arrows ±1, `PageDown`/`PageUp` ±a full page, `Ctrl+D`/`Ctrl+U` ±a half page, and `r` opens the inline comment editor on the cursor line (commentable only). Page size is estimated from the cursor line's `offsetHeight` vs the pane `clientHeight` (`diffPageLines`). `applyDiffCursor` toggles a `.diff-line.cursor` highlight (the Nth `.diff-line` element maps to line sequence N) and `scrollIntoView({block:'nearest'})`; the cursor is seeded on the first commentable line in `renderDiffView`. Hovering a diff line with the mouse moves the cursor to it (no scroll), so keyboard navigation resumes from the pointer. The handler bails when focus is in the inline comment editor's `textarea`/`input` so typing isn't hijacked.

39. **Review comment formatting (v1.14.0)**: `formatReviewComment` (`hem/pkg/ui/diff.go`, shared by the git-diff review and the Hem file viewer, and mirrored in `qew/pkg/web/static/app.js`) renders each comment in a Markdown-native layout instead of the previous flat `- Filename/- Line number/- Code/- Comment` bullet list: a `## Comment N` heading, a single `` Filename: `path`, line: N `` line (`` `path` (file header) `` for header lines with no line number, or `Line: N` when the file is unknown), the referenced source line in an unconditional fenced code block, then the user's comment as a Markdown blockquote via the new `blockquote` helper (each line prefixed with `> `, blank lines collapsed to a bare `>`). This was deferred until Copilot input was delivered over stdin (item 3 / SPEC "copilot prompt on stdin"), because the earlier inline-`-p` path truncated multi-line prompts at the first newline on Windows and made multi-line blockquoted comments unsafe to emit. The Go and JS implementations stay byte-identical so TUI and Qew reviews are indistinguishable to the agent.

40. **Last-response resolution accepts `agent_text`**: Every hem path that extracts a session's "last response" by scanning conversation turns from the end — `pollUntilIdle` (the sync reply after `create`/`continue`), the `response`/`last session` command, and the subagent-completion callback that queues `<response>…</response>` to the parent — now matches `assistant` **or** `agent_text` via the shared `isAgentResponseRole` helper (`hem/pkg/commands/commands.go`). When an agent ends its turn on a tool call (common when it delegates a review to another agent via a shelled-out CLI), moneypenny intentionally stores **no** final `assistant` turn (`handler.go` skips empty replies; the trailing narration lives as an `agent_text` train-of-thought turn). Scanning for `assistant` alone therefore skipped the agent's real last message and surfaced a **stale earlier `assistant` turn** — e.g. the message that contained the review comments — instead of what the agent actually said last. Accepting `agent_text` returns the genuine trailing narration. The "is the agent done?" check at `commands.go:2435` (sets the `reviewed`/ready flag only when the last turn is `assistant`) is deliberately left unchanged.

41. **Qew sliding session auth (v1.16.0)**: Qew login cookies now survive restarts and expire on inactivity instead of a fixed 7-day window. The HMAC signing key is no longer a per-process random value (which silently invalidated every cookie on restart — the cause of "logged out on every container reboot"); it is `sha256(seed ‖ password)` where `seed` is a persistent 32-byte random value loaded from `~/.config/james/qew/qew_secret` (created on first run with `O_EXCL`, mode `0600`, length-validated; `loadOrCreateSecret` in `qew/cmd/qew/main.go`, written to the same dir as the SSH key, which already persists across reboots). Folding the password into the key means changing `--password` still revokes existing sessions. The seed is only loaded/created when a password is set; `NewServer` takes it as a new `secretSeed []byte` parameter. The token payload is two 8-byte big-endian timestamps — `created` and `lastActive` — HMAC-signed together (16-byte payload + `.` + hex sig). `validToken` returns the parsed timestamps and rejects a token if the signature fails, if either timestamp is more than `clockSkewTolerance` (60s) in the future (clock-rollback guard), if `now-lastActive ≥ sessionInactivityTimeout` (2h sliding window), or if `now-created ≥ sessionAbsoluteTimeout` (30d hard cap, so a renewing stolen cookie can't live forever). `requireAuth` (which wraps `/`, static assets and `/api`) parses the cookie, redirects to `/login` if invalid, and otherwise — when `lastActive` is older than `tokenRefreshInterval` (10m) — re-issues the cookie with a fresh `lastActive` (preserving `created`) **before** calling the wrapped handler so the `Set-Cookie` isn't lost to an early write. The 10-minute floor avoids a `Set-Cookie` on every few-second poll, so the effective idle logout lands between 1h50m and 2h after last activity; an open tab's polling keeps the session alive indefinitely (within the 30d cap). The cookie `MaxAge` mirrors the 2h window. `sessionCookie(value)` centralizes cookie construction for both login and refresh. Long-lived WebSockets are handled too: `handleWSUpgrade` computes the session deadline (`min(lastActive+inactivity, created+absolute)`) from the upgrade request and `handleWS` arms a `time.AfterFunc` that closes the socket at that deadline, so a still-open `/ws` connection cannot outlive the session (the shipped SPA polls `/api` rather than using `/ws`, but this closes the hole for any client). `isAuthenticated` (used by `requireAuthWS`) is unchanged in contract (bool) but now delegates to the tuple-returning `parseCookie`.

### Hem Command Layer Refactoring (v0.11.0+)

The Executor (hem/pkg/commands) has been refactored to follow Single Responsibility Principle by extracting specialized managers:

**Manager Components** (`hem/pkg/commands/`):

1. **ClientManager** (`client_manager.go`): Manages transport client lifecycle and circuit breaking
   - Caches FIFO and MI6 clients per moneypenny
   - Implements cooldown mechanism (30s) to avoid hammering failed moneypennies
   - Methods: `GetClient()`, `SetCooldown()`, `IsInCooldown()`, `ClearCooldown()`, `MI6KeyPath()`

2. **CacheManager** (`cache_manager.go`): Manages moneypenny session data caching
   - Provides instant dashboard/list responses via snapshot-based cache
   - Handles background refresh coordination
   - Thread-safe with RWMutex
   - Methods: `GetSnapshot()`, `Update()`, `GetCacheTime()`, `IsRefreshing()`, `SetRefreshing()`

3. **WatchManager** (`watch_manager.go`): Manages watch/polling for sub-session state
   - Tracks parent-child session relationships
   - Maintains last known session states for transition detection
   - Methods: `AddWatcher()`, `GetWatchers()`, `RemoveWatcher()`, `SetLastState()`, `GetLastState()`, `DeleteState()`

4. **Session Helpers** (`session_helpers.go`): Extracts common session parameter resolution
   - `resolveMoneypennyForSession()`: Resolves moneypenny from name or default
   - `applyProjectDefaults()`: Applies project settings to session params
   - `applyGlobalDefaults()`: Applies global defaults for agent/path
   - `generateSessionName()`: Auto-generates names from prompts
   - `buildCreateSessionData()`: Builds moneypenny command data
   - `validatePrompt()`: Validates prompt input

**Refactored Executor** (`commands.go`):
- Reduced from 15+ fields with 3 mutexes to 6 focused fields
- Now coordinates between store, managers, and moneypenny transport
- Large command methods (CreateSession, ContinueSession) refactored to use helper functions
- Cleaner separation: orchestration logic vs. implementation details

**Benefits**:
- Each manager has single, clear responsibility
- Easier testing and mocking
- Reduced cognitive complexity
- Better thread safety encapsulation
- Simpler to extend and maintain

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

## Moneypenny Auto-Update

Moneypenny can self-update from GitHub releases (`--auto-update` flag).

1. **Architecture**: Self-contained in `moneypenny/pkg/updater/` — no dependency on hem. The updater implements `SessionChecker` interface against the handler, which queries the store for session statuses.
2. **Version comparison**: Simple semver (major.minor.patch) string comparison. The `v` prefix from tags is stripped.
3. **Binary swap**: Uses rename-based atomic swap with `.old` backup. Falls back to copy if cross-device. Platform-specific re-exec: Unix uses `syscall.Exec` (in-place replacement), Windows spawns new process and exits.
4. **Idle gating**: Update waits indefinitely for all sessions to leave `working` state. No timeout — conservative approach to avoid disrupting active agent work.
5. **MI6 resilience**: After re-exec, moneypenny's MI6 reconnect loop naturally re-establishes the connection. FIFO mode recreates pipes on startup. Sessions survive in SQLite.
6. **Companion binaries**: Also updates `mi6-client` if found alongside the moneypenny binary, since moneypenny spawns it as a subprocess for MI6 connections.
7. **Observability**: `update_status` protocol method exposes current state (checking/downloading/staged/waiting_idle/etc.) to hem and TUI.

## Versioning

Single `VERSION` file at project root. Injected at compile time via `-ldflags "-X main.Version=..."`. All components (mi6, moneypenny, hem, qew) share the same version. Semver format.
