# James - Agent Orchestration Toolkit

## Overview

James is a set of Go tools for orchestrating AI agents (Bond pun). Three components:

- **MI6** — Transport relay for remote agent communication
- **Moneypenny** — Per-host agent daemon that manages Claude Code sessions
- **Hem** — Orchestration CLI, server, and TUI

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
```

Each component has its own `go.mod` and `Makefile`. Version is injected from `VERSION` via `-ldflags`.

## Key Files

- **`hem/pkg/commands/commands.go`** — All hem command implementations (~2000 lines). This is the core business logic: session CRUD, project management, dashboard, import, diff.
- **`hem/cmd/hem/main.go`** — CLI entry point, response formatting, chat REPL, server startup.
- **`hem/pkg/ui/ui.go`** — TUI main model with view routing and key bindings.
- **`hem/pkg/ui/dashboard.go`** — Dashboard view (attention-based session grouping).
- **`hem/pkg/ui/client.go`** — TUI client wrapper for server communication.
- **`moneypenny/pkg/handler/handler.go`** — All moneypenny command handlers.
- **`moneypenny/pkg/envelope/data.go`** — Protocol data types for moneypenny commands.

## Architecture Patterns

- **Hem client/server**: CLI is a thin client; server owns state. Both talk over Unix socket (`~/.config/james/hem/hem.sock`). TUI also connects to the same socket.
- **Moneypenny protocol**: JSON envelopes over stdio or FIFO. Request/response with typed methods and error codes.
- **TUI**: Bubbletea model-view-update pattern. `ui.go` is the router; each view is its own model in a separate file. Messages flow through the top-level `Update()` which routes to the appropriate view.
- **Commands return structured data**: `commands.go` returns `protocol.Response` with typed data (TextResult, TableResult, etc). CLI and TUI both consume the same data.

## TUI Views

| View | File | Key bindings |
|------|------|-------------|
| Dashboard | `dashboard.go` | Enter=chat, a=toggle done, c=complete, d=delete, e=edit, g=diff, n=new, x=shell, m=moneypennies, p=projects, l=sessions |
| Projects | `projects.go` | Enter=open, e=edit, n=new, d=delete |
| Project detail | `dashboard.go` (reused) | Enter=chat, c=complete, d=delete, e=edit, g=diff, n=new, x=shell |
| Sessions | `sessions.go` | Enter=chat, n=new, e=edit, d=delete, g=diff, i=import, s=stop, x=shell |
| Chat | `chat.go` | Enter=send, Esc=command mode. Command: c=complete, d=delete, e=edit, g=diff, s=stop, x=shell |
| Shell | `shell.go` | Enter=run, Ctrl+U=clear, PgUp/PgDn=scroll |
| Moneypennies | `moneypennies.go` | Enter=ping, s=set default, d=delete, x=shell |
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
