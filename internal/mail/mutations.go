package mail

import (
	"context"
	"errors"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"i4dcli/internal/fault"
)

func (f FlagChanges) Validate() error {
	if f.Seen == nil && f.Flagged == nil {
		return fault.New("usage", "provide seen or flagged changes")
	}
	return nil
}
func ValidateMailbox(name string) error {
	if !validMailbox(name) {
		return fault.New("usage", "mailbox must be a nonempty exact name without control characters")
	}
	return nil
}
func (c *Client) checkWrite() error {
	if !c.allowWrite {
		return fault.New("permission_denied", "mail session does not authorize writes")
	}
	return nil
}
func (c *Client) checkedReference(id string) (reference, error) {
	if err := ValidateID(id); err != nil {
		return reference{}, err
	}
	r, _ := decodeReference(id)
	if r.Account != c.account {
		return reference{}, fault.New("conflict", "mail message ID belongs to another account")
	}
	return r, nil
}
func (c *Client) selectObject(ctx context.Context, r reference, writable, draft bool) (*imapclient.FetchMessageBuffer, error) {
	m, err := c.imap.Select(r.Mailbox, &imap.SelectOptions{ReadOnly: !writable}).Wait()
	if err != nil {
		return nil, mailError(ctx, err, "select mailbox")
	}
	if m.UIDValidity != r.Validity {
		return nil, fault.New("conflict", "mailbox UID validity changed; list messages again")
	}
	objects, err := c.imap.Fetch(imap.UIDSetNum(imap.UID(r.UID)), summaryOptions()).Collect()
	if err != nil {
		return nil, mailError(ctx, err, "inspect message")
	}
	if len(objects) == 0 {
		return nil, fault.New("not_found", "mail message no longer exists")
	}
	if len(objects) != 1 || uint32(objects[0].UID) != r.UID {
		return nil, fault.New("error", "server returned unexpected message UID")
	}
	if draft {
		found := false
		for _, flag := range objects[0].Flags {
			if flag == imap.FlagDraft {
				found = true
			}
		}
		if !found {
			return nil, fault.New("usage", "message is not marked as a draft")
		}
	}
	return objects[0], nil
}
func (c *Client) requireCapability(cap imap.Cap) error {
	if !c.imap.Caps().Has(cap) {
		return fault.New("usage", "mail server requires %s for this operation; no fallback was attempted", cap)
	}
	return nil
}

// MutationError preserves acknowledged progress and uncertain outcomes across
// CLI/MCP error envelopes. Unwrap retains the shared fault classification.
type MutationError struct {
	Result MutationResult
	Err    error
}

func (e *MutationError) Error() string                   { return e.Err.Error() }
func (e *MutationError) Unwrap() error                   { return e.Err }
func (e *MutationError) MutationOutcome() MutationResult { return e.Result }

func mutationFailure(ctx context.Context, result MutationResult, err error, step string) (MutationResult, error) {
	var status *imap.Error
	if !errors.As(err, &status) {
		result.OutcomeUnknown = true
	}
	result.Warning = "Mail " + step + " failed; inspect the message before retrying."
	if result.Partial {
		result.Warning = "Mail operation partially completed; inspect both old and new IDs before retrying."
	}
	// Network failures after a write are not labeled retryable: the server may
	// have committed the operation even if its final response was lost.
	if result.OutcomeUnknown {
		return result, &MutationError{Result: result, Err: fault.New("error", "%s Outcome may already be applied", result.Warning)}
	}
	return result, &MutationError{Result: result, Err: mailError(ctx, err, step)}
}

func (c *Client) Mark(ctx context.Context, id string, changes FlagChanges) (MutationResult, error) {
	result := MutationResult{Action: "mark", ID: id}
	if err := c.checkWrite(); err != nil {
		return result, err
	}
	if err := changes.Validate(); err != nil {
		return result, err
	}
	r, err := c.checkedReference(id)
	if err != nil {
		return result, err
	}
	result.Mailbox = r.Mailbox
	ctx, end := c.operation(ctx)
	defer end()
	if _, err := c.selectObject(ctx, r, true, false); err != nil {
		return result, err
	}
	add, remove := []imap.Flag{}, []imap.Flag{}
	for _, item := range []struct {
		value *bool
		flag  imap.Flag
	}{{changes.Seen, imap.FlagSeen}, {changes.Flagged, imap.FlagFlagged}} {
		if item.value == nil {
			continue
		}
		if *item.value {
			add = append(add, item.flag)
		} else {
			remove = append(remove, item.flag)
		}
	}
	for _, item := range []struct {
		flags []imap.Flag
		op    imap.StoreFlagsOp
	}{{add, imap.StoreFlagsAdd}, {remove, imap.StoreFlagsDel}} {
		if len(item.flags) == 0 {
			continue
		}
		err := c.imap.Store(imap.UIDSetNum(imap.UID(r.UID)), &imap.StoreFlags{Op: item.op, Silent: true, Flags: item.flags}, nil).Close()
		if err != nil {
			return mutationFailure(ctx, result, err, "mark")
		}
		result.Partial = true
	}
	result.Partial = false
	result.Completed = true
	return result, nil
}

func (c *Client) Move(ctx context.Context, id, destination string) (MutationResult, error) {
	return c.move(ctx, id, destination, "move")
}
func (c *Client) Trash(ctx context.Context, id, destination string) (MutationResult, error) {
	if err := c.checkWrite(); err != nil {
		return MutationResult{Action: "trash"}, err
	}
	if destination == "" {
		var err error
		destination, err = c.specialMailbox(ctx, imap.MailboxAttrTrash)
		if err != nil {
			return MutationResult{Action: "trash"}, err
		}
	}
	return c.move(ctx, id, destination, "trash")
}
func (c *Client) move(ctx context.Context, id, destination, action string) (MutationResult, error) {
	result := MutationResult{Action: action, PreviousID: id, Mailbox: destination}
	if err := c.checkWrite(); err != nil {
		return result, err
	}
	if err := ValidateMailbox(destination); err != nil {
		return result, err
	}
	destination = normalizeMailbox(destination)
	result.Mailbox = destination
	r, err := c.checkedReference(id)
	if err != nil {
		return result, err
	}
	if destination == r.Mailbox {
		return result, fault.New("usage", "destination must differ from source mailbox")
	}
	ctx, end := c.operation(ctx)
	defer end()
	if err := c.requireCapability(imap.CapMove); err != nil {
		return result, err
	}
	if _, err := c.selectObject(ctx, r, true, false); err != nil {
		return result, err
	}
	data, err := c.imap.Move(imap.UIDSetNum(imap.UID(r.UID)), destination).Wait()
	if err != nil {
		return mutationFailure(ctx, result, err, action)
	}
	result.Completed = true
	dest, ok := data.DestUIDs.(imap.UIDSet)
	if ok && len(dest) == 1 && dest[0].Start != 0 && dest[0].Start == dest[0].Stop && data.UIDValidity != 0 {
		result.ID = encodeReference(reference{Version: 1, Account: c.account, Mailbox: destination, Validity: data.UIDValidity, UID: uint32(dest[0].Start)})
	} else {
		result.Warning = "Message moved; server did not return its destination UID. List the destination mailbox to recover the ID."
	}
	return result, nil
}
func (c *Client) specialMailbox(ctx context.Context, attr imap.MailboxAttr) (string, error) {
	folders, err := c.Folders(ctx)
	if err != nil {
		return "", err
	}
	found := ""
	for _, f := range folders {
		for _, a := range f.Attributes {
			if a == string(attr) {
				if found != "" {
					return "", fault.New("usage", "multiple special-use mailboxes; provide an explicit destination")
				}
				found = f.Name
			}
		}
	}
	if found == "" {
		return "", fault.New("usage", "server did not identify the special-use mailbox; provide an explicit destination")
	}
	return found, nil
}

func (c *Client) Delete(ctx context.Context, id string) (MutationResult, error) {
	return c.delete(ctx, id, false)
}
func (c *Client) DeleteDraft(ctx context.Context, id string) (MutationResult, error) {
	return c.delete(ctx, id, true)
}
func (c *Client) delete(ctx context.Context, id string, draft bool) (MutationResult, error) {
	result := MutationResult{Action: "delete", PreviousID: id}
	if draft {
		result.Action = "draft_delete"
	}
	if err := c.checkWrite(); err != nil {
		return result, err
	}
	r, err := c.checkedReference(id)
	if err != nil {
		return result, err
	}
	result.Mailbox = r.Mailbox
	ctx, end := c.operation(ctx)
	defer end()
	if err := c.requireCapability(imap.CapUIDPlus); err != nil {
		return result, err
	}
	if _, err := c.selectObject(ctx, r, true, draft); err != nil {
		return result, err
	}
	return c.deleteSelected(ctx, r, result)
}
func (c *Client) deleteSelected(ctx context.Context, r reference, result MutationResult) (MutationResult, error) {
	set := imap.UIDSetNum(imap.UID(r.UID))
	if err := c.imap.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return mutationFailure(ctx, result, err, "mark deleted")
	}
	result.Partial = true
	if err := c.imap.UIDExpunge(set).Close(); err != nil {
		return mutationFailure(ctx, result, err, "targeted expunge")
	}
	result.Partial = false
	result.Completed = true
	return result, nil
}
