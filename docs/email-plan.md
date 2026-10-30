# Email implementation plan

Status: implemented through the read, organization, draft, attachment, and
explicit-send milestones, 2026-10-30. This document records the original design;
README and command/tool schemas describe the delivered interface and limits.
Automated local IMAP/SMTP tests cover the implementations. Live iCloud capability
and Sent-copy behavior validation remains separate; background sync/cache and
credential keyring support are deferred.

## Verified starting points

Apple documents IMAP at `imap.mail.me.com:993` and authenticated SMTP at
`smtp.mail.me.com:587`, using an app-specific password. The IMAP login can be the
mailbox local part or full address; SMTP requires the full iCloud Mail address.
POP is unsupported. Use implicit TLS for IMAP and require successful STARTTLS
before SMTP authentication; never fall back to plaintext. An account's calendar
login is not a reliable substitute for its mail identity. The endpoint and login
requirements come from [Apple's mail settings](https://support.apple.com/en-us/102525);
requiring STARTTLS and separating identities are implementation choices.

The repository already provides reusable permission checks in `internal/policy`,
structured faults in `internal/fault`, CLI output/schema helpers, and typed MCP
handlers. Its `internal/app.Service` and MCP factory are currently calendar/HTTP
specific. Email needs an additional application service and transport factory;
IMAP is not protected by the existing CalDAV read-only HTTP wrapper.

Candidate dependencies:

| Purpose | Candidate | Decision for implementation |
| --- | --- | --- |
| IMAP | `github.com/emersion/go-imap/v2/imapclient` | Spike and pin a tagged version behind our adapter; verify rev1 interoperability and cancellation before adopting |
| MIME | `github.com/emersion/go-message/mail` | Streaming parsing/writing with explicit byte, part, depth, and decoded-size limits |
| SMTP, later | `github.com/emersion/go-smtp` | Separate submission adapter; require TLS and retain transaction-stage information |

The [go-imap README](https://github.com/emersion/go-imap) still describes v2 as in
development; the inspected v2 tags end at `v2.0.0-beta.8`. Treat that as an API
stability tradeoff, not a stable release guarantee. Its [client documentation](https://pkg.go.dev/github.com/emersion/go-imap/v2/imapclient)
includes streaming fetch examples. [go-message](https://github.com/emersion/go-message)
provides streaming MIME and charset handling. [go-smtp](https://github.com/emersion/go-smtp)
provides SMTP clients and servers. Recheck versions when implementing and use
Dependabot afterward. Do not add these dependencies merely for this plan.

## Configuration and authentication

Reuse the XDG config/account resolver being added for calendars, including
`--account` and its precedence rules. Keep named accounts separate from permission
profiles (`readonly`, `editor`, `full`). The proposed account extension is:

```json
{
  "mail": {
    "address": "person@icloud.com",
    "imap_username": "person@icloud.com",
    "smtp_username": "person@icloud.com",
    "default_mailbox": "INBOX"
  }
}
```

This account fragment is now accepted configuration. `address`
is required for mail; it need not match the Apple Account username used by CalDAV.
Default both protocol usernames to that full mailbox address and allow explicit
IMAP override, rather than trying multiple identities after authentication errors.
Keep production hosts fixed to Apple's documented endpoints in the initial
release. Test endpoints are injected internally, never exposed to MCP input.

Reuse the existing app-password resolver (environment or hidden interactive
prompt); never put passwords in JSON, command arguments, schema output, or logs.
Named accounts do not imply independently saved secrets. In unattended use, each
process must receive the password for its selected account. A later keyring or
secret-reference feature should bind credentials to account and service explicitly.
MCP chooses one account at startup; tools cannot switch accounts or select hosts.

A future `mail auth check` can verify TLS/login and report capabilities without
fetching messages. It should be a read capability, require explicit invocation,
and never run implicitly during help, config inspection, policy, or schema.

## Milestone 1: bounded, read-only mail

Proposed commands:

```sh
i4d --account personal --readonly mail folders list --json
i4d --account personal --readonly mail messages list --mailbox INBOX --limit 50 --json
i4d --account personal --readonly mail messages list --cursor '<opaque>' --json
i4d --account personal --readonly mail messages get --id '<opaque>' --json
i4d --account personal --readonly mail messages search --mailbox INBOX \
  --from sender@example.com --subject invoice --since 2026-10-01 --limit 50 --json
```

Use typed search fields (`from`, `to`, `subject`, `since`, `before`, `unread`) joined
with AND; defer a free-form Gmail-like query language. `since` is inclusive and
`before` exclusive, defined against IMAP internal-date day semantics, not the
untrusted message `Date` header. No default body search. Search and list share a
paged result envelope: `messages`, `returned`, `next_cursor`, and `truncated`.
`truncated` means a safety cap stopped a scan; a continuation cursor means more
pages, so these must not be conflated. Do not invent an exact total count when it
has not been computed. List entries contain ID, mailbox, UID, subject, addresses,
internal date, header date if valid, flags, and encoded size; bodies are fetched
only by `get`. Human tables, TSV, JSON errors, and schema follow existing CLI UX.

Application structure:

- `internal/mail`: protocol-independent message/folder/reference models and an
  IMAP adapter. Keep dependency-specific commands behind a narrow interface.
- `internal/app/mail.go`: validate inputs and enforce exact policy before opening
  any connection; expose folders, list, get, and search operations.
- `internal/cli`: mail routing and typed flags, reusing config, faults and output.
- `internal/mcpserver`: add a mail factory alongside the calendar factory. Neither
  factory should establish the other service's connection. Retain per-call
  deadlines, output limits, and startup-only account/permission selection.

Open a bounded session per invocation/tool call initially. Enforce socket/dial
and TLS deadlines, close the connection on cancellation, drain or close unfinished
streaming commands, and cap concurrent MCP sessions. Add pooling only after a
measured need and tests for selected-mailbox/session state isolation.

### Reads must not alter messages

Select mailboxes with `EXAMINE`/read-only mode and fetch bodies with `BODY.PEEK`.
Do not use `BODY[]`, which can set `\Seen`, or `CLOSE`, which can expunge in a
writable session. The read adapter must not expose store, append, move, expunge,
or SMTP submission operations. Tests should assert the actual command transcript.
These choices follow [IMAP4rev1's read-only and fetch semantics](https://www.rfc-editor.org/rfc/rfc3501.html).

List server-reported mailbox names, delimiters and attributes. Only `INBOX` has
special case-insensitive treatment; preserve other names exactly. Reject selecting
`\Noselect` containers. Discover special-use folders when the server advertises
that support; do not assume English `Sent`, `Trash`, or `Archive` names. Mailbox
encoding belongs in the adapter, not the public CLI. An exact mailbox name is
required if discovery is ambiguous.

### Identifiers and pagination

Use an opaque, versioned reference containing account identity, exact mailbox,
UIDVALIDITY and UID. Never use message sequence numbers or `Message-ID` as stable
object keys. Validate the selected account and current UIDVALIDITY before reads
and writes; stale references produce `conflict`, missing UIDs produce `not_found`.
Moves create a new mailbox-scoped identity. These constraints follow
[IMAP's UID model](https://www.rfc-editor.org/rfc/rfc9051.html#section-2.3.1.1).

Proposed list order is descending UID (mailbox insertion order), not a claim of
chronological sorting by sender-supplied date. Capture an upper UID boundary on
page one and carry it in a versioned opaque cursor together with account,
mailbox, UIDVALIDITY, filter hash, and the next exclusive UID boundary. New arrivals
therefore do not shift existing pages. Deleted messages may disappear between
pages; return the available records without promising snapshot isolation. Validate
cursor size/schema and all bounds; cursors convey state, never authorization.

Do not fetch all message bodies or collect an unbounded UID SEARCH result to serve
a small page. Prefer capability-supported partial search results; otherwise scan
bounded descending UID windows and stream filtered UIDs, stopping at a page or
scan budget. Carry the scan boundary even if a bounded scan yields zero matches.
Explicitly handle empty mailboxes, sparse UID ranges, maximum UIDs, and exhaustion;
avoid accidental reversed IMAP ranges. A cursor and an explicit filter/mailbox
that disagree must fail instead of silently changing the query.

Keep the first release iCloud-specific. Provider selection, OAuth, and Gmail-style
labels are deferred; the narrow transport interfaces provide a future extension
point without adding those configuration choices now.

No persistent sync or message cache in milestone 1. Later, keep cursors/metadata
under XDG state and reconstructible message caches under XDG cache, never the
config file. Track account/mailbox UIDVALIDITY, UIDNEXT and highest modification
sequence where supported. Feature-detect CONDSTORE/QRESYNC; otherwise reconcile
flags and vanished messages using bounded full scans. UIDNEXT alone does not detect
flag changes or deletions. See [RFC 7162](https://www.rfc-editor.org/rfc/rfc7162.html).

### MIME and output handling

Fetch metadata first; stream requested bodies with limits on raw bytes, decoded
bytes, MIME nesting, part count, and per-part size. Surface malformed charset or
MIME data as a parse failure or explicit partial-result warning, never silently
label an incomplete message complete. Prefer `text/plain`; return HTML as inert
text with its content type. Do not render HTML, follow URLs, fetch remote images,
execute attachments, or interpret email content as MCP instructions.

Return attachment metadata initially. A later CLI-only download command should
require an explicit destination, avoid overwrite by default, reject unsafe paths,
and use generated filenames or sanitized basenames rather than MIME paths.
Attachment downloads must use PEEK and remain bounded. MCP should return bounded
content/metadata, not accept arbitrary filesystem paths. Sanitize terminal control
characters in human/TSV views while preserving escaped values in JSON.

## Policy and MCP mapping

Add these exact commands and corresponding tools in milestone 1:

| Exact permission | Capability | MCP tool |
| --- | --- | --- |
| `mail.folders.list` | Read | `mail_folders_list` |
| `mail.messages.list` | Read | `mail_messages_list` |
| `mail.messages.get` | Read | `mail_messages_get` |
| `mail.messages.search` | Read | `mail_messages_search` |

All three permission profiles allow reads; embedded and runtime restrictions
continue to intersect. Exact lists remain literal. Internal mailbox discovery
is part of an authorized operation, not an additional permission grant. Examples
for an isolated MCP mail reader:

```sh
i4d --account personal --readonly \
  --enable-commands-exact mcp,mail.messages.list,mail.messages.get \
  mcp --allow-tool mail_messages_list,mail_messages_get
```

Adding these read tools to the default MCP registry broadens which account data a
generic `--readonly mcp` process exposes. Document that behavior and recommend
exact `--allow-tool` lists for deployments intended to remain calendar-only.
Permission denial must occur before credentials or network activity. Tool schemas
accept bounded IDs/cursors/filters and cannot accept account, host, password,
permission, output path, or force flags.

## Milestone 2: mailbox organization

Add explicit `mail.messages.mark`, `mail.messages.move`, and
`mail.messages.trash` operations after read-only interoperability is proven.
Proposed capabilities: mark is Edit; move/trash are Delete because they remove a
message from the source view. Thus `editor` cannot move or trash mail. Use explicit
flag assignments (`--seen=true`, `--flagged=false`) rather than toggles. Prefer
server-side MOVE when available; return the new UID reference when reported.

Do not silently fall back to COPY + delete + global EXPUNGE. Such a fallback has
multiple failure points and can remove messages marked deleted by another client.
Defer permanent purge, bulk deletion, folder creation/rename/delete, and automatic
archive destinations. If permanent deletion is added, require targeted UID EXPUNGE
support and a separate exact permission, plus confirmation/`--force`. The targeted
expunge and UID mapping mechanisms are defined by
[UIDPLUS](https://www.rfc-editor.org/rfc/rfc4315.html).

MCP mutations require `--allow-write`; move/trash additionally require
`--allow-delete` and operator force authorization, matching the deletion model.
Use CONDSTORE preconditions where supported for flag updates; otherwise document
that there is no ETag-equivalent guarantee and never replace all flags blindly.
Dry runs remain local, report unverified remote state, and never contact IMAP.

## Milestone 3: compose and send

Introduce separate `mail.drafts.create`, `mail.drafts.get`, `mail.drafts.edit`,
`mail.drafts.delete` and `mail.send` operations. Start with one-message plaintext
composition; add attachments, reply/forward semantics, and alias selection only
with dedicated tests. Draft storage uses IMAP APPEND and is itself a write. Draft
replacement is not atomic: track append success before deleting an older version
and report partial completion explicitly.

The current `editor` profile grants generic Create/Edit capabilities; do not
classify SMTP submission as ordinary Create. Add a distinct `Send` capability
and explicit MCP startup gate. Proposed profile behavior: `readonly` denies all
writes, `editor` permits draft creation/editing but denies send/delete, and `full`
permits send. Sending still requires recipient/content review in a terminal or
explicit `--force` for automation; `--no-input` never authorizes sending. MCP adds
startup `--allow-send` alongside `--allow-write` and operator `--force`. Send stays
out of the default tool list, and tool input cannot authorize itself.

Validate addresses, header injection, body encoding, message size, sender identity,
and Bcc handling before connecting. Keep envelope recipients separate from visible
headers. Never put Bcc recipients in transmitted headers. Use a configured sender
identity rather than arbitrary caller-supplied From addresses in the first release.
A local dry run displays recipients, sender, subject, attachment metadata and
validation status, clearly stating that server authorization was not checked.

Record the submission phase: before DATA, recipients accepted/rejected, awaiting
final DATA response, and acknowledged acceptance. If any RCPT fails, default to
aborting before DATA and report the rejected recipients; don't partially send
without an explicit feature. An affirmative final DATA reply means SMTP accepted
responsibility, not confirmed inbox delivery. A lost connection around the final
reply leaves delivery uncertain. Return an explicit `delivery_unknown` outcome
with a new non-retryable error class rather than the current generic `retryable`
network classification. See [SMTP response semantics](https://www.rfc-editor.org/rfc/rfc5321.html#section-4.2.5).

Never auto-retry an uncertain send. A stable Message-ID and local operation ID help
investigation but do not provide server-side idempotency. A later private XDG state
ledger can block repeated local operation IDs; it cannot guarantee exactly-once
submission across crashes or other clients. Verify whether iCloud creates a Sent
copy during the live spike before implementing APPEND-to-Sent. If submission
succeeds but saving a Sent copy fails, return `sent: true` with a separate
`sent_copy` warning; do not invite resending the message.

## Delivery sequence and acceptance criteria

1. **Adapter spike:** pinned library, fake TLS IMAP server, login/capability/read-only
   select/UID fetch, strict host verification and cancellation. With an explicitly
   supplied test account, record iCloud capabilities and verify reading leaves
   unread flags unchanged. Redact credentials and message content from transcripts.
2. **Read CLI:** folders/list/get/search, account selection, bounded cursors,
   fault mapping, help/schema and output examples. No body download on list.
3. **Read MCP:** shared application methods, exact policy and typed tools,
   concurrency/output limits. Verify calendar-only tool allowlists stay isolated.
4. **Mail organization:** independently reviewed mark/move/trash semantics,
   capability negotiation, preconditions and partial failure reporting.
5. **Compose/send:** transport-stage errors, explicit authorization, recipient
   review, sent-copy behavior and no automatic retry. Sending acceptance tests use
   a local SMTP server; live send requires a deliberately chosen recipient.

Automated tests must cover rejected policy producing zero dial attempts; missing
credentials; invalid certificates and missing STARTTLS; rev1 capability fallback;
EXAMINE/PEEK transcripts; non-ASCII folders and nonselectable parents; UIDVALIDITY
changes; expunges and new arrivals between pages; malformed/cross-account cursors;
sparse searches and bounded zero-result pages; MIME bombs, malformed charsets,
header/terminal injection; streaming cancellation; MCP denied tools and output
limits; and SMTP disconnects before/after DATA acceptance. Keep fake-server tests
hermetic and race-enabled. Run `make ci` and a six-target GoReleaser snapshot after
adding dependencies; a production mail adapter should preserve CGO-free builds.

Before sending ships, resolve sender aliases/custom domains, Sent-copy behavior,
and the exact draft workflow against a test mailbox. Before background sync ships,
resolve retention/cache privacy and conflict reconciliation. These decisions do
not block the read-only milestone. Recurring calendar writes and invitations are
separate work; this email plan does not add them implicitly.
