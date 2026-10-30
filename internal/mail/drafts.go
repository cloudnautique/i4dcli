package mail

import (
	"context"
	"github.com/emersion/go-imap/v2"
	"i4dcli/internal/fault"
)

func ValidateDraft(raw []byte) error {
	if len(raw) == 0 || len(raw) > maxRawBody {
		return fault.New("usage", "draft MIME must contain 1 byte to 2 MiB")
	}
	if err := decodeBody(raw, &Message{}); err != nil {
		return fault.New("usage", "draft MIME is malformed or exceeds decoding limits")
	}
	return nil
}
func (c *Client) CreateDraft(ctx context.Context, mailbox string, raw []byte) (MutationResult, error) {
	result := MutationResult{Action: "draft_create", Mailbox: mailbox}
	if err := c.checkWrite(); err != nil {
		return result, err
	}
	if err := ValidateDraft(raw); err != nil {
		return result, err
	}
	if mailbox == "" {
		var err error
		mailbox, err = c.specialMailbox(ctx, imap.MailboxAttrDrafts)
		if err != nil {
			return result, err
		}
	}
	if err := ValidateMailbox(mailbox); err != nil {
		return result, err
	}
	mailbox = normalizeMailbox(mailbox)
	result.Mailbox = mailbox
	ctx, end := c.operation(ctx)
	defer end()
	if err := c.requireCapability(imap.CapUIDPlus); err != nil {
		return result, err
	}
	return c.appendDraft(ctx, mailbox, raw, result)
}
func (c *Client) appendDraft(ctx context.Context, mailbox string, raw []byte, result MutationResult) (MutationResult, error) {
	cmd := c.imap.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft}})
	if _, err := cmd.Write(raw); err != nil {
		_ = c.conn.Close()
		return mutationFailure(ctx, result, err, "append draft")
	}
	if err := cmd.Close(); err != nil {
		_ = c.conn.Close()
		return mutationFailure(ctx, result, err, "append draft")
	}
	data, err := cmd.Wait()
	if err != nil {
		return mutationFailure(ctx, result, err, "append draft")
	}
	result.Completed = true
	if data.UID != 0 && data.UIDValidity != 0 {
		result.ID = encodeReference(reference{Version: 1, Account: c.account, Mailbox: mailbox, Validity: data.UIDValidity, UID: uint32(data.UID)})
	} else {
		result.Warning = "Draft saved, but server omitted its UID; list the destination mailbox before retrying."
	}
	return result, nil
}
func (c *Client) EditDraft(ctx context.Context, id string, raw []byte) (MutationResult, error) {
	result := MutationResult{Action: "draft_edit", PreviousID: id}
	if err := c.checkWrite(); err != nil {
		return result, err
	}
	if err := ValidateDraft(raw); err != nil {
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
	if _, err := c.selectObject(ctx, r, true, true); err != nil {
		return result, err
	}
	result, err = c.appendDraft(ctx, r.Mailbox, raw, result)
	if err != nil {
		return result, err
	}
	result.Completed = false
	result.Partial = true
	if result.ID == "" {
		result.Warning = "New draft saved without a UID; old draft retained. Inspect the drafts mailbox before retrying."
		return result, &MutationError{Result: result, Err: fault.New("error", "%s", result.Warning)}
	}
	// APPEND is acknowledged before the original is touched. There is no atomic
	// replacement in baseline IMAP; preserve the new ID in every partial result.
	result, err = c.deleteSelected(ctx, r, result)
	if err != nil {
		return result, err
	}
	result.Completed = true
	result.Partial = false
	return result, nil
}
