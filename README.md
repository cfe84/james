# James — Agent Orchestration Toolkit

James is a set of tools for orchestrating AI agents (Bond pun intended). It lets you manage, monitor, and coordinate multiple agent sessions across local and remote machines.

## Components

- **Hem** — CLI, TUI, and chat REPL for managing agent sessions
- **Moneypenny** — Per-host daemon that runs agent sessions (Claude Code, GitHub Copilot)
- **MI6** — Transport relay for remote agent communication
- **Qew** — Web UI for remote access via MI6

## Install

### Mac (Apple Silicon)

```bash
curl -sL https://github.com/cfe84/james/releases/latest/download/james-darwin-arm64.tar.gz | tar xz -C $HOME/.local/bin --strip-components=1
```

### Linux (amd64)

```bash
curl -sL https://github.com/cfe84/james/releases/latest/download/james-linux-amd64.tar.gz | tar xz -C $HOME/.local/bin --strip-components=1
```

### Linux (arm64)

```bash
curl -sL https://github.com/cfe84/james/releases/latest/download/james-linux-arm64.tar.gz | tar xz -C $HOME/.local/bin --strip-components=1
```

### Windows (amd64)

```powershell
Invoke-WebRequest https://github.com/cfe84/james/releases/latest/download/james-windows-amd64.zip -OutFile james.zip; Expand-Archive james.zip -DestinationPath $env:LOCALAPPDATA\james -Force; $env:PATH += ";$env:LOCALAPPDATA\james\james-windows-amd64"; Remove-Item james.zip
```

To make it permanent, add `%LOCALAPPDATA%\james\james-windows-amd64` to your PATH.

## Quick Start

```bash
# Start the hem server
hem server &

# Register a local moneypenny
hem add moneypenny --name local --type fifo --address ~/.config/james/moneypenny

# Start moneypenny
moneypenny --auto-update &

# Create a session
hem create session -m local "Fix the login bug"

# Open the TUI
hem ui
```

## Building from Source

```bash
make build    # Build all components
make test     # Run all tests
make install  # Install to ~/bin
```

Requires Go 1.25+. Each component (`hem/`, `moneypenny/`, `mi6/`, `qew/`) has its own `go.mod`.

## License

MIT
