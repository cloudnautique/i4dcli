# Review notes

- Preserve untouched vCard content lines when editing contacts. The pinned
  go-vcard encoder does not round-trip quoted parameter values safely; a label
  containing a colon or semicolon can corrupt the property value when re-encoded.
  Cover quoted parameters and folded lines in both vCard 3.0 and 4.0 regressions.
  Rebuild edited structured names with fresh fields: go-vcard.Name embeds the
  original parameterized/grouped Field, so SetName alone retains unsafe metadata.
  Preserve the logical name components that the caller did not replace.
- Update scheduling revision metadata on every changed VEVENT, including detached
  recurrence exceptions. Organizer changes advance each component's own SEQUENCE
  and timestamps; attendee responses update timestamps without advancing organizer
  SEQUENCE. Unchanged exceptions must retain their revisions so clients can tell
  genuine instance updates from unrelated series changes.
