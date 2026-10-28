# i4dcli

A Go CLI for iCloud calendar discovery and event management. Uses
[go-webdav](https://github.com/emersion/go-webdav) for CalDAV. No Mac or local
Calendar app is required. Lists calendar collections and events in a date range,
and creates, edits, and deletes standalone events.

## Build and run

Requires Go 1.24 or newer.

```sh
go build -o bin/i4d ./cmd/i4d
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

Both boundaries are required: `--from` is inclusive and `--to` exclusive, so the
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

Use the `path` returned by create or `events list --json` to edit or delete:

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

Delete executes immediately; there is no interactive confirmation. All three
commands support `--json` (an object with `action` and `path`), `--username`, and
`--timeout`. Creation refuses to overwrite an existing resource. Edit and delete
fetch the original object and use its strong ETag in a conditional write; a
concurrent change causes an error instead of overwriting it. Servers without a
strong ETag are rejected for these operations.

Recurring events, including individual occurrences, cannot yet be edited or
deleted. These commands reject them without writing. Creating recurrence rules
and managing meeting invitations are not supported.

## Development

```sh
go test ./...
go vet ./...
```

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
or alter deletion behavior. Delete still executes immediately when permitted.

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

The CLI and future MCP handlers can use the shared `internal/app` operations and
`internal/policy` command registry. MCP, dry-run previews, structured error codes,
and date shortcuts are planned follow-up work, not part of this milestone.
