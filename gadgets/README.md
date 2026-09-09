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
gadgets agents message id [--body text]
gadgets subagents list
gadgets subagents create [--name name] [--agent agent] [--model model] [--path path]
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

Reads return up to 64000 Unicode characters by default. For very large imported
notes, follow `data.next_offset` using `--offset` until it is absent; `--limit`
accepts 1–64000 characters. `data.characters` is the full node size. Paging does
not change stored content and allows reading notes larger than the response cap.

`memory set` and both `message` commands read stdin verbatim unless `--body`
is present (including an explicitly empty value). `subagents create` always reads
its prompt from stdin. `notify` reads stdin when text is omitted.
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
| subagents create | `subagents.create` | `{"prompt":"...", "name":"...", "agent":"...", "model":"...", "path":"..."}`; optional flags omitted unless supplied |
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

## Development

```sh
make build
make test
```
