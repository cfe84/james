# James — Agent Orchestration Toolkit

James is a set of tools for orchestrating AI agents (Bond pun intended). It lets you manage, monitor, and coordinate multiple agent sessions across local and remote machines.

## Components

- **Hem** — CLI, TUI, and chat REPL for managing agent sessions
- **Moneypenny** — Per-host daemon that runs agent sessions (Claude Code, GitHub Copilot, OpenCode)
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

## MI6 Authentication

MI6 does not use your default `~/.ssh` identity. Each James client creates and
uses its own ECDSA key in its data directory:

| Client | Private key |
| --- | --- |
| Moneypenny | `~/.config/james/moneypenny/moneypenny_ecdsa` |
| Hem remote client | `~/.config/james/hem/hem_ecdsa` |
| Qew direct-MI6 mode | `~/.config/james/qew/qew_ecdsa` |
| Manual `mi6-client` | The path provided through `--key` |

Install the corresponding public key (`.pub`) in the relay's `authorized_keys`.
Keys permitted to manage relay keys additionally belong in `admin_keys`.

Client authentication is separate from relay identity: every remote MI6 client
also requires the relay server's `SHA256:...` fingerprint through
`--server-fingerprint` (or the higher-level `--mi6-server-fingerprint` flag).
This pin verifies that the connection reached the intended relay.

## Building from Source

```bash
make build    # Build all components
make test     # Run all tests
make install  # Install to ~/bin
```

Requires Go 1.25+. Each component (`hem/`, `moneypenny/`, `mi6/`, `qew/`) has its own `go.mod`.

## License

MIT
