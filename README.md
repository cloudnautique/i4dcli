# i4dcli

A Go CLI for iCloud calendar discovery and event management. Uses
[go-webdav](https://github.com/emersion/go-webdav) for CalDAV. No Mac or local
Calendar app is required. Lists calendar collections and events in a date range,
and creates, edits, and deletes standalone events.

## Build and run

Requires Go 1.25.5 or newer (the pinned MCP library’s minimum).

```sh
make
./bin/i4d calendars list --username you@icloud.com
```

Enter an **Apple app-specific password** at the hidden terminal prompt, not your
normal Apple Account password. Create one at <https://account.apple.com/> under
Sign-In and Security → App-Specific Passwords (requires two-factor authentication).
See [Apple's instructions](https://support.apple.com/en-us/102654).
Credentials are not saved by this CLI.

For scripts, provide `ICLOUD_USERNAME` and `ICLOUD_APP_PASSWORD` through your
secret manager/environment. There is deliberately no password command-line flag.
For an interactive Bash session without putting the password in shell history:

```sh
export ICLOUD_USERNAME='you@icloud.com'
read -r -s -p 'App-specific password: ' ICLOUD_APP_PASSWORD; printf '\n'
export ICLOUD_APP_PASSWORD
./bin/i4d calendars list --json
unset ICLOUD_APP_PASSWORD
```

`--json` returns an array with `name`, `path`, `components`, and optional
`description`. Empty results are `[]`. Without it, output is a table. Calendar
names need not be unique; paths distinguish calendars. Components describe what
each collection supports (for example VEVENT or VTODO).

Discovery starts at `https://caldav.icloud.com/`, finds the current user's
principal and calendar home, then lists calendar collections. Authentication is
restricted to HTTPS on Apple's CalDAV hostname and numbered CalDAV shards,
including redirects. `--timeout 30s` bounds the entire network operation.
Errors go to stderr with a nonzero exit status.

## List events from multiple calendars

```sh
./bin/i4d events list --username you@icloud.com \
  --calendar "bill" --calendar "bill and kelley" \
  --from 2026-10-28 --to 2026-11-04
```

Repeat `--calendar` to combine calendars. Names match in full, case-insensitively;
you can also use an exact path from `calendars list`. Repeated selections query
the calendar once. Ambiguous names require a path; unknown names and task-only
collections produce errors. At least one calendar is required.

For an explicit range both boundaries are required: `--from` is inclusive and `--to` exclusive, so the
example covers October 28 through November 3. Accepts `YYYY-MM-DD` or RFC3339
timestamps with an explicit offset. Dates and floating event times use the
machine's local timezone by default. Use `--timezone America/New_York` (or your
IANA timezone) to set date boundaries and displayed times explicitly, especially
when running on a remote machine configured in UTC.

Results include events overlapping the range, sorted by start time, with the
source calendar shown. `--json` includes calendar and event paths, UID,
recurrence ID, summary, start/end, all-day flag, location, and status. All-day
start/end values are dates and the end is exclusive; timed values include an
offset. Cancelled events returned by the server remain visible with their status.

Recurrences are expanded by the CalDAV server using the standard
[calendar-data expand request](https://www.rfc-editor.org/rfc/rfc4791.html#section-9.6.5),
so individual occurrences and moved exceptions can appear. A server returning
unexpanded recurrence rules produces an error instead of misleading results.
Unknown event timezone IDs also produce an error. If any selected calendar fails,
the command fails without printing a partial event list.

For troubleshooting, add `--verbose` to `events list`. Stderr shows the resolved
range/timezone, selected calendar paths, returned object and event counts, and
how many events overlap the requested range. It does not print credentials or
event content. JSON on stdout remains unchanged.

## Create, edit, and delete events

Each write command selects exactly one calendar by name or path. Create a timed
event with RFC3339 timestamps (including offsets):

```sh
./bin/i4d events create --calendar "bill" --summary "Lunch" \
  --start 2026-10-29T12:00:00-04:00 --end 2026-10-29T13:00:00-04:00 \
  --location "Cafe" --description "Bring notes"
```

For an all-day event, use dates for both boundaries, for example
`--start 2026-10-29 --end 2026-10-30` for October 29 only. The end is exclusive
and must be later than the start. Timed events are stored in UTC.

Use the `path` returned by create or the PATH column in `events list` to inspect,
edit, or delete an event:

```sh
./bin/i4d events edit --calendar "bill" --path /ACCOUNT/calendars/CALENDAR/EVENT.ics \
  --summary "Lunch with Sam" --location ""
./bin/i4d events delete --calendar "bill" --path /ACCOUNT/calendars/CALENDAR/EVENT.ics
```

Edit accepts `--summary`, `--location`, `--description`, and a paired
`--start`/`--end`. Omitted fields are preserved, including alarms and custom
properties. An empty location or description clears that field. The summary
cannot be blank. To reschedule or switch between timed and all-day, supply both
boundaries. Event paths must belong directly to the selected calendar.

Delete asks for confirmation in an interactive terminal. Scripts must pass
`--force`; `--no-input` alone never authorizes deletion. `--force` cannot override
a permission profile or exact command restriction. All three commands support `--json` (an object with `action` and `path`), `--username`, and
`--timeout`. Creation refuses to overwrite an existing resource. Edit and delete
fetch the original object and use its strong ETag in a conditional write; a
concurrent change causes an error instead of overwriting it. Servers without a
strong ETag are rejected for these operations.

Recurring events, including individual occurrences, cannot yet be edited or
deleted. These commands reject them without writing. Creating recurrence rules
and managing meeting invitations are not supported.

## Development

```sh
make           # Build bin/i4d (same as make build)
make test      # Run all tests with the race detector
make vet       # Run go vet
make generate  # Run go generate ./...
make lint      # Check gofmt formatting and run pinned Staticcheck
make ci        # Generate, build, vet, lint, then test
```

`make ci` runs targets sequentially, including when invoked with `make -j ci`,
so generation finishes before compilation or checks. No `go:generate` directives
exist yet; the generation target is ready for future generators. Lint checks
formatting without rewriting files. Staticcheck is pinned in the Makefile and
runs through `go run`, so no separate installation is required; the first run
needs network access. Its current version requires Go 1.26+, which Go can fetch
automatically when toolchain auto-selection is enabled. The application itself
requires Go 1.25.5+. Race tests require a supported platform and C toolchain.

Override the output path with `make BINARY=/path/to/i4d` or the Go executable
with `make GO=/path/to/go`.

Protocol tests use a local fake CalDAV server; they require no Apple credentials.
A real account is still needed to validate live iCloud connectivity. The calendar
adapter is separate from the CLI so it can be reused by a future server.

## Permissions and automation

Global flags work before or after the command. Existing command names and JSON
success output remain supported; `events update` is an alias for `events edit`.
Use `i4d help events edit` or `i4d events edit --help` for command options.

```sh
# Read-only invocation; discovery and event queries still work.
./bin/i4d --readonly events list --calendar bill \
  --from 2026-10-28 --to 2026-11-04

# Permit reads and event creation/editing, but no deletion.
./bin/i4d --profile editor events update --calendar bill \
  --path /ACCOUNT/calendars/CALENDAR/EVENT.ics --summary "Lunch with Sam"

# Narrow automation to exactly these user operations, without prompts.
./bin/i4d --no-input --enable-commands-exact calendars.list,events.list \
  calendars list --json

# Inspect permissions locally, without credentials or network requests.
./bin/i4d --profile editor --readonly policy --json
```

| Profile | List calendars/events | Create events | Edit events | Delete events |
| --- | --- | --- | --- | --- |
| `readonly` | Yes | No | No | No |
| `editor` | Yes | Yes | Yes | No |
| `full` (default) | Yes | Yes | Yes | Yes |

`--readonly` further restricts any profile. Commands denied by a profile or exact
allowlist fail before credentials are requested or any network activity occurs.
The shared application layer checks the same policy, and read-only HTTP transport
blocks mutating requests, including redirects. CalDAV `PROPFIND` and `REPORT`
remain available for discovery and queries.

Exact command lists use dotted names and accept the `events.update` alias as
`events.edit`. Parent names and wildcards are rejected. Internal discovery needed
by an allowed event operation does not require a separate `calendars.list` grant.
`policy` remains available for inspecting permissions even with an exact list;
its output lists allowed commands and reasons for blocked commands. Help menus
show only permitted operations.

`--no-input` requires credentials in the environment (or `--username` for the
account) and never prompts for a password. It does not grant write permissions
or authorize deletion. Unattended deletion additionally requires `--force`.

Environment defaults are `I4D_READONLY`, `I4D_NO_INPUT`, `I4D_PROFILE`, and
`I4D_ENABLE_COMMANDS_EXACT`. Boolean values accept `true`/`false` or `1`/`0`.
Explicit flags override these defaults; an empty exact-list flag clears the
runtime allowlist. Invalid profile or command names and nonempty lists containing
only commas/whitespace are rejected.

### Embedded profiles

Build a dedicated binary whose profile cannot be loosened by runtime flags or
environment variables. Run these commands from the repository root:

```sh
./scripts/build-profile.sh readonly bin/i4d-readonly
./scripts/build-profile.sh editor bin/i4d-editor
./bin/i4d-readonly policy --json
```

Embedded and runtime permissions intersect: `--profile full`, `--readonly=false`,
or an exact grant cannot enable a command forbidden by the embedded profile.
Errors identify the restriction responsible. These are local execution controls;
they do not change the credentials' server permissions or prevent a caller with
those credentials from using another program.

The CLI and MCP handlers use the shared `internal/app` operations and
`internal/policy` command registry. Both enforce the same application permissions;
the MCP server additionally restricts the tools it exposes.


## Calendar shortcuts and inspection

```sh
./bin/i4d events list --calendar bill --today --timezone America/New_York
./bin/i4d events list --calendar bill --tomorrow
./bin/i4d events list --calendar bill --days 7
./bin/i4d events get --calendar bill --path /ACCOUNT/calendars/CALENDAR/EVENT.ics --json
```

Choose exactly one of `--today`, `--tomorrow`, `--days N`, or the explicit
`--from`/`--to` pair. `--days` accepts 1–3660 and starts at today's local midnight.
Shortcuts use calendar days in `--timezone` (default `Local`), including daylight
saving transitions; they do not assume every day is 24 hours.

`events get` returns the resource path, ETag, and an `events` array with details,
including descriptions and recurrence rules. A recurring resource may contain a
master event and exceptions; this command inspects them without expanding them.
Recurring resources still cannot be edited or deleted. Object reads are limited
to 10 MiB. Paths must belong directly to the selected calendar.

## Previews and machine-readable output

```sh
./bin/i4d --dry-run --json events create --calendar bill --summary "Lunch" \
  --start 2026-10-29 --end 2026-10-30
./bin/i4d --dry-run events delete --calendar bill \
  --path /ACCOUNT/calendars/CALENDAR/EVENT.ics
./bin/i4d events list --calendar bill --today --plain
./bin/i4d --readonly schema --json
```

`--dry-run` validates a mutation locally and emits its proposed action and fields.
It makes **no network requests and requires no credentials**. Its JSON reports
`dry_run: true`, `validation: "local"`, and `remote_checked: false`. Calendar
existence, path membership, server permissions, recurrence, and ETags have not
been checked. It does not generate a new UID or reserve an event path. Previews
still require permission for the underlying mutation; read-only profiles reject
mutations even with `--dry-run`.

`--plain` emits TSV with a header, including on empty lists. Tabs, line breaks,
and control characters in display values become spaces to keep one record per
line. `--json` preserves original field values. These two output modes are
mutually exclusive. Existing JSON success shapes remain unchanged; ordinary
event tables now include a PATH column.

`schema` always emits JSON, with `schema_version`, permitted commands and aliases,
flags, constraints, output formats, exit codes, and the effective safety policy.
Flag descriptions come from the same definitions used to parse commands. It
requires no credentials and does not verify server access. `policy --json`
remains available for a smaller permissions-only view.

On failure, JSON mode emits an object such as:

```json
{"error":{"code":6,"class":"permission_denied","message":"command blocked by policy"}}
```

Human error messages go to stderr. An error envelope is not appended to partial
stdout output or an MCP protocol stream. Always check the process exit status.

| Exit code | Class | Meaning |
| --- | --- | --- |
| 0 | Success | Operation succeeded |
| 1 | `error` | Unclassified failure |
| 2 | `usage` | Invalid arguments or configuration flags |
| 4 | `auth_required` | Server rejected authentication |
| 5 | `not_found` | Calendar or event not found |
| 6 | `permission_denied` | Local policy or server denied access |
| 7 | `rate_limited` | Server returned HTTP 429 |
| 8 | `retryable` | Network failure, timeout, or server failure |
| 9 | `conflict` | Concurrent change or resource conflict |
| 10 | `config` | Required credentials are missing |
| 11 | `confirmation_required` | Deletion lacks operator authorization |
| 130 | `cancelled` | Operation interrupted or confirmation declined |

There are no automatic retries. After a timeout on a mutation, inspect the
calendar before retrying: the server may already have applied the change.

## MCP server

`i4d mcp` serves MCP over stdio. The client launches the binary and uses the
standard initialize, tools/list, and tools/call protocol. It exposes typed tools,
not a shell-command bridge. Tool inputs cannot change the account, permissions,
force flag, dry-run mode, or server limits. Unknown input fields are rejected.

```sh
# Inspect enabled tools and their schemas without credentials or a running server.
./bin/i4d --readonly mcp --list-tools

# Serve only calendar reads.
./bin/i4d --readonly mcp --allow-tool calendars_list,events_list,events_get

# Permit creating/editing events while deletion remains blocked.
./bin/i4d --profile editor mcp --allow-write

# Explicitly authorize deletion as well.
./bin/i4d --force mcp --allow-write --allow-delete

# Preview permitted writes without credentials or network activity.
./bin/i4d --dry-run mcp --allow-write
```

| Tool | Required startup authorization |
| --- | --- |
| `calendars_list` | Read permission; enabled by default |
| `events_list` | Read permission; enabled by default |
| `events_get` | Read permission; enabled by default |
| `events_create` | `--allow-write` and command permission |
| `events_edit` | `--allow-write` and command permission |
| `events_delete` | `--allow-write`, `--allow-delete`, and command permission; actual deletion also requires operator `--force` |

`--allow-tool` accepts exact names, comma-separated or repeated. It narrows the
surface; it cannot grant a forbidden capability. Empty lists and unknown names
are errors. Profiles and exact command grants still apply. For example, an exact
read grant for MCP would be `--enable-commands-exact mcp,events.list`. `schema` and
`policy` remain available regardless of this list. Tools blocked by policy are
omitted; a configuration that enables no tools fails at startup.

A generic MCP client configuration:

```json
{
  "command": "/absolute/path/to/i4d",
  "args": ["--readonly", "mcp"]
}
```

The server inherits `ICLOUD_USERNAME` and `ICLOUD_APP_PASSWORD` from its process
environment. It never prompts, even without `--no-input`. Credentials are checked
when a tool needs network access; `--list-tools` and local write previews need
none. Stdout is reserved for MCP; logs go to stderr. `--plain` is unsupported,
and `--json` is only accepted with `mcp --list-tools`.

Tool results contain a structured `result` object and a JSON text fallback for
older clients. Application failures set MCP `isError` and return the same
`error.code`, `error.class`, and `error.message` fields as the CLI. Invalid
protocol or schema inputs use the MCP SDK's protocol errors.

`events_list` takes a `calendars` array and the same date-range choices as the CLI.
Other event tools take one `calendar`; inspection, edit, and delete also take
`path`. Create/edit accept `summary`, `location`, `description`, `start`, and
`end`. These are literal values, not filenames. Omitted editable fields are
preserved; empty location/description clears them.

Each tool call has its own `--timeout` (default 30s); this is not the server's
lifetime. `--max-results` defaults to 1000 (maximum 10000); list callers may use
`limit` to narrow it further. Lists return `returned`, `matched`, and `truncated`
metadata. Truncation is explicit; narrow the range to retrieve fewer events.
The server bounds returned results, not the number of records the CalDAV server
may send while answering a query.

`--max-output-bytes` caps each JSON result payload (default 1 MiB, maximum 10 MiB),
excluding the duplicate text fallback and protocol framing. Oversized read
results fail with guidance to narrow the request. If a mutation has already
succeeded but its result is too large, the server returns a compact success with
`completed: true` and `output_omitted: true` rather than suggesting a retry.

## GitHub CI and releases

Pushes to branches and pull requests run `make ci` and `goreleaser check`
without uploading build artifacts. Version tags matching `v*` trigger the release
workflow. Use version names such as `v1.2.3` or `v1.2.3-rc.1`:

```sh
git tag v1.2.3
git push origin v1.2.3
```

After checks pass, GoReleaser OSS cross-compiles all six targets concurrently
on one Ubuntu runner (`--parallelism=6`). Go builds directly with `CGO_ENABLED=0`;
no QEMU, Docker, macOS runner, Windows runner, or target C compiler is involved. Go dependencies and build caches are
cached by `setup-go`. Release jobs use the stable Go toolchain.

Once checks and every build pass, the workflow creates a GitHub release with
generated notes and these assets:

| OS | Architectures | Archive |
| --- | --- | --- |
| macOS (`darwin`) | `arm64`, `x86_64` | `.tar.gz` |
| Linux | `arm64`, `x86_64` | `.tar.gz` |
| Windows | `arm64`, `x86_64` | `.zip` |

Names follow `i4d_v1.2.3_linux_x86_64.tar.gz`. Each archive contains `i4d` (or
`i4d.exe`) and the README. `SHA256SUMS` contains checksums of all six archives.
Go calls x86_64 `amd64`; the release filenames use `x86_64`.
Prerelease tags such as `v1.2.3-rc.1` create prereleases. Only tag runs publish
release assets; branch and pull request workflows do not upload artifacts.

With GoReleaser v2 installed, validate and build all six archives locally
without publishing:

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish --parallelism=6
```

Release settings live in `.goreleaser.yaml`. Snapshot builds use a development
version and write into ignored `dist/`; `--clean` replaces that directory.
Cross-compilation verifies builds; it does not execute tests on the foreign
OS/architecture. Native race-enabled tests run in the CI job.
The macOS and Windows binaries are not distribution-signed or notarized.

Dependabot checks Go modules and GitHub Actions weekly. Minor and patch updates
are grouped per ecosystem; major updates get separate pull requests. Action
references are pinned to full commit SHAs, including the GoReleaser action.
The action installs the latest GoReleaser v2. Dependabot changes go through CI
and are not automatically merged.
