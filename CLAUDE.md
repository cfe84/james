# James - Agent Orchestration Toolkit

## Overview

James is a set of Go tools for orchestrating AI agents (Bond pun). Four components:

- **MI6** — Transport relay for remote agent communication
- **Moneypenny** — Per-host agent daemon that manages agent sessions (Claude Code, GitHub Copilot)
- **Hem** — Orchestration CLI, server, and TUI
- **Qew** — Web UI for remote access via MI6

## Project Structure

```
james/
├── mi6/                    # Transport relay
│   ├── cmd/mi6-client/     # Local client binary
│   ├── cmd/mi6-server/     # Remote server binary
│   ├── pkg/{protocol,auth,session,transport}/
│   └── internal/{batch,config}/
├── moneypenny/             # Agent daemon
│   ├── cmd/moneypenny/     # Daemon binary
│   └── pkg/{envelope,store,agent,handler}/
├── hem/                    # Orchestration CLI + TUI
│   ├── cmd/hem/            # CLI binary (main.go has CLI client, server startup, chat REPL)
│   └── pkg/
│       ├── cli/            # Verb+noun command parser
│       ├── commands/        # All command implementations (commands.go is the big one)
│       ├── hemclient/      # Unix socket client
│       ├── protocol/       # Request/Response types
│       ├── server/         # Unix socket server
│       ├── store/          # SQLite (moneypenny registry, session tracking, projects)
│       ├── transport/      # FIFO and MI6 clients for talking to moneypennies
│       ├── output/         # Output formatting (json, text, table, tsv)
│       └── ui/             # TUI (bubbletea + lipgloss)
├── qew/                    # Web UI for remote access
│   ├── cmd/qew/            # Binary (SSH key gen, MI6 connect, HTTP server)
│   └── pkg/web/            # HTTP/WebSocket server, MI6 client, embedded static files
├── VERSION                 # Single version for all components
├── Makefile                # Top-level build orchestration
├── SPEC.md                 # Feature specification
└── ARCHITECTURE.md         # Technical decisions
```

## Building & Testing

```bash
make build          # Build all components
make test           # Test all components
make install        # Install binaries to ~/bin or similar

# Individual components:
cd hem && go build ./...
cd moneypenny && go build ./...
cd mi6 && go build ./...
cd qew && go build ./...
```

Each component has its own `go.mod` and `Makefile`. Version is injected from `VERSION` via `-ldflags`.

## Key Files

- **`hem/pkg/commands/commands.go`** — All hem command implementations (~2000 lines). This is the core business logic: session CRUD, project management, dashboard, import, diff.
- **`hem/cmd/hem/main.go`** — CLI entry point, response formatting, chat REPL, server startup.
- **`hem/pkg/ui/ui.go`** — TUI main model with view routing and key bindings.
- **`hem/pkg/ui/dashboard.go`** — Dashboard view (attention-based session grouping).
- **`hem/pkg/ui/wizard.go`** — 3-step create session wizard (moneypenny → path → form).
- **`hem/pkg/ui/client.go`** — TUI client wrapper for server communication (Unix socket or MI6).
- **`hem/pkg/server/mi6.go`** — MI6 control channel listener for hem server.
- **`hem/pkg/hemclient/client.go`** — Sender interface with SocketSender and MI6Sender implementations.
- **`hem/pkg/commands/notification.go`** — Sound notification on task completion.
- **`moneypenny/pkg/handler/handler.go`** — All moneypenny command handlers.
- **`moneypenny/pkg/envelope/data.go`** — Protocol data types for moneypenny commands.

## Architecture Patterns

- **Hem client/server**: CLI is a thin client; server owns state. Both talk over Unix socket (`~/.config/james/hem/hem.sock`) or MI6 transport. TUI also connects to the same socket or via MI6. Server can accept commands from an MI6 control channel (`--mi6-control`).
- **Moneypenny protocol**: JSON envelopes over stdio or FIFO. Request/response with typed methods and error codes.
- **TUI**: Bubbletea model-view-update pattern. `ui.go` is the router; each view is its own model in a separate file. Messages flow through the top-level `Update()` which routes to the appropriate view.
- **Commands return structured data**: `commands.go` returns `protocol.Response` with typed data (TextResult, TableResult, etc). CLI and TUI both consume the same data.

## TUI Views

| View | File | Key bindings |
|------|------|-------------|
| Dashboard | `dashboard.go` | Enter=chat, a=toggle done, c=complete, d=delete, e=edit, g=diff, n=new (wizard), x=shell, m=moneypennies, p=projects, l=sessions |
| Projects | `projects.go` | Enter=open, e=edit, n=new, d=delete |
| Project detail | `dashboard.go` (reused) | Enter=chat, c=complete, d=delete, e=edit, g=diff, n=new (wizard), x=shell |
| Sessions | `sessions.go` | Enter=chat, n=new (wizard), e=edit, d=delete, g=diff, i=import, s=stop, x=shell |
| Chat | `chat.go` | Enter=send, ^J=newline, Esc=command mode, Del=delete right, Alt+←/→=word nav. Command: c=complete, d=delete, e=edit, g=diff, s=stop, t=schedule, x=shell |
| Shell | `shell.go` | Enter=run, Ctrl+U=clear, PgUp/PgDn=scroll |
| Moneypennies | `moneypennies.go` | Enter=ping, s=set default, d=delete, x=shell |
| Create wizard | `wizard.go` | 3-step: select moneypenny → browse path → fill form. Esc=back/cancel, Enter=select/submit, Tab=confirm path (step 2) |
| Create session | `create.go` | Tab=next field, Enter=submit |
| Edit session | `edit.go` | Tab=next field, Enter=save |
| Create project | `projects.go` | Tab=next field, Enter=submit |
| Edit project | `projects.go` | Tab=next field, Enter=save |
| Import session | `importform.go` | Tab=next field, Enter=import |
| Git diff | `diff.go` | Arrow keys=scroll, PgUp/PgDn=page |
| Styles | `styles.go` | Color definitions (orange primary, not violet) |

## Conventions

- Go for everything. No external CLI framework (custom verb+noun parser).
- SQLite with WAL mode for all persistence.
- Bubbletea for TUI with `glamour` for markdown rendering (use `WithStylePath("dark")`, NOT `WithAutoStyle()` which breaks the TUI with OSC escape sequences).
- Paste support in TUI: check `msg.Type == tea.KeyRunes` (not string length).
- Session deletion is resilient: if moneypenny is unreachable, local tracking is still cleaned up.
- Projects are a hem-level concept (not known to moneypenny). Session-project mapping is in hem's SQLite.
- `--async` flag on create/continue returns immediately; hem polls moneypenny for completion when sync.
- **Documentation**: When making any functional change, update both `SPEC.md` (feature specification) and `ARCHITECTURE.md` (technical decisions). These must always reflect the current state of the codebase.
