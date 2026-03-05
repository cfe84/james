We are building James, a set of tools used to orchestrate agents (see the pun yet?).

# Some basic requirements

- Keep track of technical decisions in an ARCHITECTURE.md file. Always check if it needs updates
- Keep the spec up to date, I might ask you to do changes out of the spec, they should be reflected here.

# MI6

MI6 is a transport abstraction that allows, by creating a central place that all hosts can reach, to communicate between these hosts.

- We'll have agents running remotely. They checking in to their boss through MI6.
- MI6 is simply a delocalized proxy where agents can check-in. It serves as transport.
- It is composed of two pieces: a local client `mi6-client` that connects to a unique, remote server `mi6-server`. Mi-6 server will run in a container somewhere.
- It is built using golang.
- mi-6 client opens a session to mi6-server. That session is authenticated using ssh-keys, and has a session-id determined by the client.
- mi-6 server has a list of authorized_keys, we should support ecdsa and rsa.
- communication between client and server is encrypted using the ssh-key
- 2 or more clients will open the same session on mi-6 server, then communicate through it.
- Communication on the client happens through stdio. Client should batch some of the text coming through stdin, then send to the server. Server then broadcasts to all _other_ connected clients to their stdout.
- For the client, let's support `mi6-client mi6.servername.com/session_id` as a valid command, in addition to flags.
- We should be able to pass the ECDSA key as environment variable to mi6-client, or directly in a `--key-value` path.
- Add a `--generate-key` that generates a key.

# Moneypenny

Moneypenny is a client deployed on each host, which handles agent sessions. It is built using Go.

- Interfacing is done using stdio. Commands are sent to Moneypenny using stdin, and it outputs responses on stdout.
- `-v` flag enables verbose logging to stderr: commands received, agent executions, responses sent.
- Moneypenny can either interface directly on stdio (for local use), or open a connection to an mi6-server. `moneypenny --mi6 mi6.servername.com/this_hosts_name` ; using host name as the session id.
- Moneypenny has a local store based on sqlite, to keep track of everything it needs (sessions, conversation history, parameters).
- When integrating through mi6, moneypenny creates an ECDSA ssh-key, stores it locally, and uses that to authenticate with mi6. Use `moneypenny --show-public-key` to output the public key for adding to an mi6-server's authorized_keys.
- We'll be using a protocol based on json envelopes for commands and responses:
  - Request: `{ "type": "request", "method": "method_name", "request_id": "id", "data": {} }`
  - Success response: `{ "type": "response", "status": "success", "request_id": "id", "data": {} }`
  - Error response: `{ "type": "response", "status": "error", "request_id": "id", "error_code": "ERROR_CODE", "data": { "message": "human-readable description" } }`
- Standardized error codes:
  - `SESSION_NOT_FOUND` - session_id does not exist
  - `SESSION_ALREADY_EXISTS` - create_session with a session_id that already exists
  - `SESSION_NOT_IDLE` - continue_session when session is in working state
  - `SESSION_NOT_WORKING` - stop_session when session is not in working state
  - `AGENT_NOT_FOUND` - the requested agent binary (e.g. claude) is not installed
  - `INVALID_PATH` - the provided path does not exist
  - `AGENT_ERROR` - the agent subprocess crashed or returned an error
  - `INVALID_REQUEST` - malformed command or missing required fields
  - `INTERNAL_ERROR` - unexpected internal error

Method: **create_session**: creates a new session with an agent. For now, we'll always use claude. Format of the data is `{ "agent": "claude", "system_prompt": "a system prompt for the agent", "yolo": boolean indicating if the session should be started with --dangerously-skip-permissions, "prompt": "prompt for the agent", "session_id": "GUID used for communication about that session id", "name": "a session name", "path": "the path where to start the agent" }`

- If session_id already exists, return `SESSION_ALREADY_EXISTS` error.
- When a new session is started, moneypenny saves all the parameters in its local storage, and invokes the agent following the parameters in data of the method.
- It requests output-format to be json, invokes the corresponding agent with the correct parameters.
- When invoking claude, it uses the session_id passed by the caller as session id.
- It waits for the response, then saves it in its local store and sends back the text response wrapped in the response envelope.
- Moneypenny should keep track of a session state, notably: working, when a prompt was sent to the agent and we are waiting for the response ; and idle, when the response was received.

Method: **continue_session**: continues a session that was started with an agent. Data for the request contains the session_id and the new prompt to send to the agent: `{ "session_id": "id", "prompt": "the new prompt" }`

- Moneypenny then simply runs the prompt, using the session_id to continue the conversation. It reuses the parameters previously sent when session was created.
- Moneypenny should reject continue_session commands when the session is not idle (`SESSION_NOT_IDLE` error).

Method: **list_sessions**: returns the list of sessions, with their respective status, name and ids.

Method: **get_session**: returns details about the provided session_id, including all parameters, status, and all prompts and responses (stored in sqlite).

Method: **delete_session**: deletes a session. If it is in working state, the agent subprocess is killed first.

Method: **stop_session**: stops the agent subprocess for a working session. Session state goes back to idle, allowing continue_session to be called afterwards. Returns `SESSION_NOT_WORKING` error if session is not in working state.

Method: **get_version**: returns the version of moneypenny

Memory: Moneypenny creates a memory file for all the sessions it handles. It give the path to that memory files to the agent in the system prompt and asks it to write anything it needs to remember there, and dumps the memory into the system prompt in each call.

Local deployment: add a `--local` convenience flag that allows moneypenny to run in local mode through fifo.

- We invoke moneypenny with `--fifo FOLDER`
- Moneypenny creates two fifo, `moneypenny-in` and `moneypenny-out`
- Then it uses these fifo to get input and produce output

Versioning: A single `VERSION` file at the project root is the source of truth for all components (mi6, moneypenny, hem). Semver format (e.g. `0.1.0`). The version is injected at compile time via Go's `-ldflags "-X main.Version=..."`. Each component's Makefile reads from `VERSION`. Bump minor for new features, patch for fixes.

# Hem

Hem handles the overall agent management. It connects to all its moneypenny instances, sends work there, and retrieve the work result. It acts as an interface for all of them. It is built using Go.

## Architecture: Client/Server

Hem uses a client/server architecture over a Unix domain socket (`~/.config/james/hem/hem.sock`).

- **Hem Server** (`hem server`): A long-running daemon that owns the SQLite store, moneypenny transport connections, and all orchestration logic. It listens on the Unix socket and processes requests.
- **Hem CLI** (all other commands): A thin client that parses the command, sends a JSON request to the server over the Unix socket, receives the response, and formats the output.
- The server must be running for any command to work. If the server is not running, the CLI prints an error.
- This architecture allows the server to maintain persistent state, open connections, and handle async operations, while the CLI is lightweight and stateless.
- Future clients (UI, web) can connect to the same socket.

### Internal protocol (over Unix socket)

Line-delimited JSON, one request/response per connection:
- Request: `{ "verb": "create", "noun": "session", "args": ["prompt text", "--name", "test"] }`
- Success response: `{ "status": "ok", "data": { ... } }`
- Error response: `{ "status": "error", "message": "human-readable error" }`
- `data` is always structured JSON. The CLI formats it according to `--output-type`.

## General

- Hem is a cli tool
- It uses commands with verbs and names, similar to kubectl, e.g. `hem add moneypenny`, `hem create session`, `hem list sessions`, etc. All names should support singular and plural for all verbs (eg both `hem add moneypenny` and `hem add moneypennies` are correct)
- For all commands we can specify an `--output-type` or `-o` which might be either `json`, `text`. If the expected output is a table, we can specify `tsv` or `table` (formats as table)
- The server stores state in a sqlite instance (moneypenny registry, session-to-moneypenny mapping).
- For MI6 transport, hem auto-generates an ECDSA SSH key (same approach as moneypenny), stored in its data directory. Use `hem show-public-key` to output the key for adding to mi6-server's authorized_keys.
- `hem set-default moneypenny -n NAME` sets the default moneypenny. Session commands use this default when `-m` is not specified.
- `hem set-default agent VALUE` sets the default agent (used by `create session` when `--agent` is not specified, fallback: `claude`).
- `hem set-default path VALUE` sets the default working directory (used by `create session` when `--path` is not specified, fallback: `.`).
- `hem get-default agent|path|moneypenny` shows the current default for a given key.
- `hem list defaults` shows all configured defaults.

## Server

`hem server [-v]` — starts the hem server daemon.

- The server listens on a Unix domain socket at `~/.config/james/hem/hem.sock`.
- `-v` enables verbose logging to stderr (requests received, responses sent).
- The server must be running before any other command can be used.
- On shutdown (SIGINT/SIGTERM), the server removes the socket file and exits cleanly.
- Only one server instance can run at a time (binding to the socket fails if another is already running).

## Moneypenny management

Hem has a list of moneypennies it can use. Each instance has a unique name and a transport reference (FIFO or MI6).

### Add

`hem add moneypenny --name|-n NAME [flags]`

- Name must be unique.
- Add a local moneypenny:
    - Local instances use FIFO for communication.
    - `--fifo-folder FOLDER` (expects `moneypenny-in` and `moneypenny-out` in FOLDER)
    - Or `--fifo-in INPUT_FIFO` and `--fifo-out OUTPUT_FIFO` for custom paths.
- Add an MI6 moneypenny:
    - `--mi6 mi6.server.example.com/session_id`
- A transport reference (FIFO or MI6) is required.
- On add, hem validates connectivity by calling `get_version` on the moneypenny.

Example: `hem add moneypenny -n local --fifo-folder ~/moneypenny-fifo`

### List

`hem list moneypennies` — lists all moneypennies with name, type (fifo/mi6), and connection info.

### Ping

`hem ping moneypenny -n NAME` — pings a moneypenny using `get_version`, displays version and round-trip time.

### Remove / Delete

`hem remove moneypenny -n NAME` or `hem delete moneypenny -n NAME` — removes the reference.

### Set default

`hem set-default moneypenny -n NAME` — sets the default moneypenny for session commands.

## Sessions

Hem manages sessions on moneypennies. It tracks which moneypenny each session lives on in its local SQLite. By default, session commands wait for the agent to complete; use `--async` to return immediately.

### Create

`hem create session -m|--moneypenny NAME PROMPT [flags]`

- `-m` is optional if a default moneypenny is set.
- Hem generates a session_id (UUID) and sends `create_session` to the moneypenny.
- By default: waits for the agent to complete, prints the session_id and the response.
- With `--async`: prints the session_id and returns immediately without waiting.
- Flags: `--agent NAME` (default "claude"), `--name NAME` (session name, default empty), `--system-prompt TEXT`, `--yolo` (skip permissions), `--path PATH` (working directory for the agent).

### Continue

`hem continue session SESSION_ID PROMPT` or `hem continue session --session-id ID PROMPT`

- Sends `continue_session` to the moneypenny that owns this session.
- By default: waits for the agent to complete, prints the response.
- `--async`: return immediately without waiting.

### Stop

`hem stop session SESSION_ID` — stops a working session (kills the agent, session goes back to idle).

### Delete

`hem delete session SESSION_ID` — deletes a session (kills agent if working, removes from moneypenny and local tracking).

### State

`hem state session SESSION_ID` — shows the current state of the session (idle/working).

### Last

`hem last session SESSION_ID` — shows the last assistant response.

### Show

`hem show session SESSION_ID` — shows session parameters (agent, system_prompt, yolo, path, name, status).

### History / Log

`hem history session SESSION_ID [-n N]` or `hem log session SESSION_ID [-n N]` — shows conversation history. `-n` limits to last N turns (default: all).

### List

`hem list sessions [-m MONEYPENNY_NAME]` — lists all sessions across all moneypennies. `-m` filters by moneypenny.
