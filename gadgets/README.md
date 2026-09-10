# Gadgets

Thin, dependency-free Go client for session-scoped Moneypenny tools. The daemon,
not this client, authenticates sessions, enforces capabilities and permissions,
and implements atomic memory batches.

## Commands

```text
gadgets memory get [path] [--offset N] [--limit N]
gadgets memory list [path]
gadgets memory search query
gadgets memory set [path] [--body text]
gadgets memory batch
gadgets memory delete path [--recursive]
gadgets memory revisions [path]
gadgets agents list
gadgets agents create [--name name] [--agent agent] [--model model] [--path path] [--moneypenny name] [--traits names-or-IDs]
gadgets agents message id [--body text]
gadgets traits list
gadgets traits get ID
gadgets traits edit ID
gadgets subagents list
gadgets subagents create [--name name] [--agent agent] [--model model] [--path path] [--moneypenny name] [--traits names-or-IDs]
gadgets subagents message id [--body text]
gadgets schedule list
gadgets schedule create (--cron expr | --at timestamp) --prompt text
gadgets schedule delete id
gadgets notify [text]
gadgets help
gadgets version
```

Omitted memory paths mean root (`""`). Quote multiword arguments. Flags can precede
or follow positional arguments; `--` ends flag parsing, and `--flag=value` is
supported. `--recursive=false` explicitly disables recursion. No session,
endpoint, credential, or permission override flags are accepted.

### Top-level agent creation (opt-in)

`agents create` reads a nonempty prompt from stdin and creates an independent
top-level session on the caller's Moneypenny by default, or a different
registered Moneypenny named with `--moneypenny`. It returns the new `session_id`
with `async: true`; it does not wait for the initial task to finish. Optional
name, agent, model, and path flags use ordinary Hem session-creation defaults
when omitted. It does not inherit the caller's project, parent, or yolo setting.

Operators enable the separate `create_agents` permission (default **false**)
with `--gadget-create-agents=true` or the TUI/Qew controls. The created session
inherits the creator's current gadget permissions, including this grant; agents
cannot override permissions during creation. Discovery/messaging still requires
the separate `agents` grant. Creating an agent does not make it a child or
authorize later management of it.

Moneypenny binds the source from the caller's credential. Hem checks the fresh
creation permission and calls `create session --from=<caller-session-id>`,
preserving the creator's ID and resolved display name on the initial prompt.
Agents cannot provide `--from`, parent IDs, routing credentials, or permissions.
`subagents create` accepts the same target override; it remains a child of the
authenticated caller even when it runs on another Moneypenny.

```sh
printf 'Maintain the project documentation' | gadgets agents create --name docs
```

### Shared traits (opt-in)

Both `agents create` and `subagents create` accept `--traits "Name,trait-id"`.
Selection uses Hem's existing name/ID resolver, deduplicates traits, composes their
text into the new system prompt, and persists the assignments. Unknown traits
fail before creation. `--traits=""` explicitly selects none. Omitting the flag
preserves the existing defaults: default-enabled traits for top-level agents,
none for subagents (neither inherits the creator's selection).
Selecting traits for a new session uses the relevant creation permission,
not the separate shared-trait editing permission described below.

`traits list` lists existing shared definitions. `traits get ID` returns the full
definition; IDs or exact names are accepted. `traits edit ID` replaces only its
prompt body, reading the complete text verbatim from stdin (empty stdin clears
the body). Agents may edit only traits assigned to their own session; list and
get can view all definitions. It returns the updated definition. No `--body`
flag is accepted:

```sh
gadgets traits list
gadgets traits get 'Clean code'
printf 'Reuse existing implementations.\n' | gadgets traits edit 'Clean code'
```

The `traits` permission defaults to **false**, including legacy sessions. Operators
grant it with `hem update session SESSION_ID --gadget-traits=true`, or the existing
TUI/Qew permission controls; create/copy/subsession commands also accept that flag.
Gadget-created children inherit the parent's current permissions. Revocation is
checked on every request by Moneypenny and again through a fresh daemon lookup
at Hem. Hem must be reachable via the operator-configured route.

Definitions are shared across agents managed by that Hem. Editing affects their
**future use**, not prompts already composed into sessions. Names, default-enabled
settings, session assignments and unrelated definitions remain unchanged. These
commands cannot create/delete traits, rename them, change defaults, assign traits,
or manage sessions.

Memory reads return up to 64000 Unicode characters by default. For very large imported
notes, follow `data.next_offset` using `--offset` until it is absent; `--limit`
accepts 1–64000 characters. `data.characters` is the full node size. Paging does
not change stored content and allows reading notes larger than the response cap.

`memory set` and both `message` commands read stdin verbatim unless `--body`
is present (including an explicitly empty value). `agents create` and
`subagents create` always read their prompt from stdin. `notify` reads stdin when text is omitted.
`memory batch` reads a JSON array of objects with string `path` and `body`
fields; the entire array is submitted in one request. It never performs
individual writes or retries.

```sh
printf 'Project context\n' | gadgets memory set project
printf '[{"path":"project","body":"Context"},{"path":"project/tasks","body":"Todo"}]' |
  gadgets memory batch
printf 'Investigate failing tests' | gadgets subagents create --name tests
gadgets schedule create --cron '0 9 * * 1-5' --prompt 'Review open tasks'
gadgets notify 'Investigation complete'
```

## HTTP contract

The daemon supplies `JAMES_GADGETS_URL` (the complete POST endpoint, including
its path) and `JAMES_GADGETS_TOKEN` (a session-scoped bearer token). Only HTTP URLs
using literal loopback IPs or `localhost` are accepted. `localhost` is pinned to
`127.0.0.1`; IPv6 daemons should provide `[::1]` explicitly. URL credentials,
query strings, fragments, proxies, and redirects are disallowed.

Requests include `Content-Type: application/json`, `Accept: application/json`,
and `Authorization: Bearer <token>`. Body:

```json
{"method":"memory.set","data":{"path":"project","body":"Context"}}
```

| Command | Method | `data` |
| --- | --- | --- |
| memory get/list/revisions | `memory.get` / `memory.list` / `memory.revisions` | `{"path":""}`; get also accepts numeric `offset` and `limit` |
| memory search | `memory.search` | `{"query":"..."}` |
| memory set | `memory.set` | `{"path":"","body":"..."}` |
| memory batch | `memory.batch` | `{"entries":[{"path":"...","body":"..."}]}` |
| memory delete | `memory.delete` | `{"path":"...","recursive":false}` |
| agents/subagents list | `agents.list` / `subagents.list` | `{}` |
| agents/subagents message | `agents.message` / `subagents.message` | `{"id":"...","body":"..."}` |
| traits list | `traits.list` | `{}` |
| traits get | `traits.get` | `{"id":"..."}` |
| traits edit | `traits.edit` | `{"id":"...","body":"..."}`; complete string body required, empty allowed |
| agents/subagents create | `agents.create` / `subagents.create` | `{"prompt":"...", "name":"...", "agent":"...", "model":"...", "path":"...", "traits":"Name,id"}`; optional flags omitted unless supplied; empty traits selects none; caller identity and permissions cannot be supplied |
| schedule list | `schedule.list` | `{}` |
| schedule create | `schedule.create` | `{"cron":"...", "prompt":"..."}` or `{"at":"...", "prompt":"..."}` |
| schedule delete | `schedule.delete` | `{"id":"..."}` |
| notify | `notify` | `{"text":"..."}` |

Responses must be a single JSON envelope:

```json
{"success":true,"data":{"example":"result"}}
{"success":false,"error":{"code":"permission_denied","message":"Capability not enabled"}}
```

Successful envelopes are emitted as JSON to stdout and return exit code 0.
Failure envelopes go to stderr and return exit code 1, preserving daemon error
codes. Client errors use the same envelope with codes `usage`, `configuration`,
`request_too_large`, `transport`, `protocol`, or `output`. Invalid envelopes,
non-2xx HTTP status, oversized responses, and network failures return nonzero.
Help is plain text; version is `{"version":"1.78.0"}` (or the injected build version).

Requests (including stdin) are limited to 1 MiB, responses to 4 MiB, response
headers to 32 KiB. HTTP calls have a 30-second deadline and a 5-second connection
timeout. The client does not retry operations.

Trait list results reuse Hem's table shape (`headers`, `rows`), with prompt previews.
Trait get/edit results contain `id`, `name`, `prompt`, and `enabled_by_default`.
Traits use the same request/response limits, without paging. Missing traits or
Hem route failures return an error envelope; denied requests never fall back to
operator management commands.

## Development

```sh
make build
make test
```
