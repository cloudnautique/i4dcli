# iCloud Contacts

Contacts use CardDAV discovery at `https://contacts.icloud.com/`. The same named
account `username`, `--username` / `ICLOUD_USERNAME`, and Apple app-specific password
used for calendars apply here. Passwords stay in the environment or hidden prompt;
there is no separate saved contacts secret. `--account` resolves the account once
per CLI invocation or MCP startup. Address-book selection is always explicit.

```sh
i4d addressbooks list --json
i4d contacts list --addressbook Personal --limit 100 --json
i4d contacts search --addressbook Personal --query Ada --json
i4d contacts get --addressbook Personal --path /ACCOUNT/carddav/BOOK/CONTACT.vcf --json

i4d contacts create --addressbook Personal --name 'Ada Lovelace' \
  --given-name Ada --family-name Lovelace --email ada@example.com \
  --phone '+44 1234 567890' --organization 'Example' --birthday 1815-12-10

i4d contacts edit --addressbook Personal --path /ACCOUNT/carddav/BOOK/CONTACT.vcf \
  --note 'Updated notes' --email ada@example.com --email work@example.com

i4d contacts edit --addressbook Personal --path /ACCOUNT/carddav/BOOK/CONTACT.vcf \
  --clear-phones --note ''

i4d contacts delete --addressbook Personal --path /ACCOUNT/carddav/BOOK/CONTACT.vcf
```

Address books match a full case-insensitive name or exact path from discovery.
Ambiguous names require a path. Contact paths must be direct members of the
selected address book; other hosts, sibling books, query strings and traversal
paths are rejected. Paths identify resources; display names need not be unique.

Create requires a nonempty formatted `--name`. Editable fields are formatted name,
given/family names, email and phone lists, organization, note, birthday and URL
lists. Repeat `--email`, `--phone`, or `--url` to **replace that entire property
list**. Clear one with `--clear-emails`, `--clear-phones`, or `--clear-urls`; combining
a clear flag with new values is rejected. Empty organization, note or birthday
clears that field. Birthday is a full `YYYY-MM-DD` date. Structured given/family
names cannot contain semicolons or newlines.

Omitted fields retain their vCard values, parameters and groups, including postal
addresses, photos, Apple custom labels and other unexposed fields. Changed list
properties receive fresh values without the old labels/parameters. `contacts get`
returns normalized fields plus the complete parsed `properties` map for inspection.
Create writes a vCard 3.0 resource with a generated UID; edits support existing
vCard 3.0/4.0 resources and keep their version. The library may normalize serialization
while preserving unmodified field semantics. Group membership and photo editing
are not exposed as dedicated commands.

Create uses `If-None-Match: *` and never overwrites an existing resource. Edit and
delete fetch the current vCard and require a strong ETag, then use `If-Match`.
A concurrent change returns `conflict`; a missing/weak ETag also refuses the write.
There are no automatic retries. After a timeout on a write, inspect the address
book because the server may have applied the change before the connection failed.
These behaviors use [CardDAV](https://www.rfc-editor.org/rfc/rfc6352) and HTTP
conditional requests through the existing go-webdav adapter.

`readonly` permits discovery/list/search/get; `editor` also permits create/edit;
`full` additionally permits deletion. Exact commands are `addressbooks.list` and
`contacts.list`, `contacts.search`, `contacts.get`, `contacts.create`,
`contacts.edit`, `contacts.delete`. Policy is checked before credentials or network
activity. Delete requires terminal confirmation or explicit `--force`; `--no-input`
alone does not authorize deletion.

```sh
i4d contacts create --addressbook Personal --name Ada --dry-run --json
i4d --readonly contacts list --addressbook Personal --plain
```

Mutation previews validate local input only, require no credentials, make no network
requests, and report `remote_checked: false`. They do not generate a UID or reserve
a resource path. Address-book existence, path membership and ETags are not verified
in a preview. Read-only profiles still reject mutation previews.

List/search return `{contacts, returned, truncated}`. Limit defaults to 100 and is
bounded at 1000. The client requests at most limit+1 entries from the server, sorts
the returned subset by name/path, and reports truncation; it does not claim a globally
sorted, paginated snapshot. Search matches formatted name, email, phone or organization
using server-side contains filters. Narrow the query if truncated; there is no cursor
or unbounded export mode. A server may reject an overly broad query instead of returning
a partial result. Discovery is capped at 1000 books, each HTTP response at 16 MiB,
and individual contact reads/writes at 1 MiB. Human/TSV tables sanitize control
characters; JSON preserves escaped field values. List truncation notices go to stderr
outside JSON mode. Get and mutation human output use readable JSON; `--plain` uses a
single JSON value in a TSV column.

The HTTP transport sends credentials only to HTTPS `contacts.icloud.com` and its
numbered `pN-contacts.icloud.com` shards on the default/443 port. Discovery follows
verified redirects while retaining DAV methods. Writes use the discovered shard and
reject redirects so conditional mutations cannot silently change destinations.
Readonly clients additionally block writes at the transport layer.

## MCP

The corresponding tools are `addressbooks_list`, `contacts_list`, `contacts_search`,
`contacts_get`, `contacts_create`, `contacts_edit`, and `contacts_delete`. Read tools
are enabled by default when allowed by policy; create/edit require `--allow-write`,
and delete additionally requires `--allow-delete` and operator `--force`.
An existing calendar-only deployment should keep its exact `--allow-tool` list.

```sh
i4d --readonly mcp --allow-tool addressbooks_list,contacts_list,contacts_search,contacts_get
i4d --profile editor mcp --allow-write --allow-tool contacts_create,contacts_edit
i4d --dry-run mcp --allow-write --allow-tool contacts_create
```

Tools take `addressbook`, resource `path` where applicable, and typed contact fields.
List/search use `limit`; search uses `query`. Changes are top-level `name`,
`given_name`, `family_name`, `emails`, `phones`, `organization`, `note`, `birthday`,
and `urls`. Omitted values are preserved; empty arrays clear list fields. Tools
cannot change accounts, endpoints, credentials or permission gates. The server's
`--max-results` further caps requested list limits; `--max-output-bytes` applies to
results including full contact properties. Inspect schemas with `mcp --list-tools`.

Protocol tests use a local fake CardDAV server, including ETag conflicts, preserving
unmodified properties, path confinement, redirect trust and response bounds. Live
Apple account interoperability requires a deliberately supplied test account and
has not been exercised by these tests.
