package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"i4dcli/internal/config"
	"i4dcli/internal/fault"
	"i4dcli/internal/mail"
	"i4dcli/internal/policy"
)

func mailSettings(g globals, account config.Account) (config.Mail, error) {
	settings := config.Mail{}
	if account.Mail != nil {
		settings = *account.Mail
	}
	if g.mailAddressSet {
		if !strings.EqualFold(g.mailAddress, settings.Address) {
			settings = config.Mail{}
		}
		settings.Address = g.mailAddress
	}
	if settings.Address == "" && !g.mailAddressSet {
		login := strings.TrimSpace(g.username)
		if login == "" && !g.usernameSet {
			login = strings.TrimSpace(account.Username)
		}
		_, domain, ok := strings.Cut(login, "@")
		if ok {
			switch strings.ToLower(domain) {
			case "icloud.com", "me.com", "mac.com":
				settings.Address = login
			}
		}
	}
	if settings.Address == "" {
		return config.Mail{}, fault.New("config", "set mail.address, ICLOUD_MAIL_ADDRESS, or --mail-address; an iCloud mail address cannot be inferred from this account login")
	}
	if err := settings.Validate(); err != nil {
		return config.Mail{}, fault.New("config", "%s", err)
	}
	if settings.IMAPUsername == "" {
		settings.IMAPUsername = settings.Address
	}
	if settings.SMTPUsername == "" {
		settings.SMTPUsername = settings.Address
	}
	if settings.DefaultMailbox == "" {
		settings.DefaultMailbox = "INBOX"
	}
	return settings, nil
}

func mailCredentials(g globals, account config.Account, noInput bool, stderr io.Writer) (mail.Options, error) {
	settings, err := mailSettings(g, account)
	if err != nil {
		return mail.Options{}, err
	}
	password, err := credentials(settings.IMAPUsername, noInput, stderr)
	if err != nil {
		return mail.Options{}, err
	}
	identity, _ := json.Marshal([]string{strings.ToLower(settings.Address), settings.IMAPUsername})
	return mail.Options{Username: settings.IMAPUsername, Password: password, AccountID: string(identity)}, nil
}

type mailOptions struct {
	list                    mail.ListOptions
	id                      string
	destination, output     string
	attachment              int
	seen, flagged, replyAll bool
	compose                 mail.Compose
	changes                 mail.FlagChanges
	attachments             calendarNames
	to, cc, bcc             calendarNames
}

func mailFlags(c policy.Command, out io.Writer) (*flag.FlagSet, *mailOptions) {
	fs := flag.NewFlagSet("i4d "+strings.ReplaceAll(c.Name, ".", " "), flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprintf(out, "Usage: i4d %s\n", c.Usage); fs.PrintDefaults(); printGlobalHelp(out) }
	o := &mailOptions{}
	switch c.Name {
	case "mail.messages.list", "mail.messages.search":
		fs.StringVar(&o.list.Mailbox, "mailbox", "", "exact mailbox; account default or INBOX for a new query")
		fs.StringVar(&o.list.Cursor, "cursor", "", "opaque continuation cursor from a previous page")
		fs.IntVar(&o.list.Limit, "limit", 50, "maximum messages per page (1–100)")
		if c.Name == "mail.messages.search" {
			fs.StringVar(&o.list.Filter.From, "from", "", "sender filter")
			fs.StringVar(&o.list.Filter.To, "to", "", "recipient filter")
			fs.StringVar(&o.list.Filter.Subject, "subject", "", "subject filter")
			fs.StringVar(&o.list.Filter.Since, "since", "", "inclusive IMAP internal date: YYYY-MM-DD")
			fs.StringVar(&o.list.Filter.Before, "before", "", "exclusive IMAP internal date: YYYY-MM-DD")
			fs.BoolVar(&o.list.Filter.Unread, "unread", false, "only messages without the Seen flag")
		}
	}
	switch c.Name {
	case "mail.messages.get", "mail.messages.mark", "mail.messages.move", "mail.messages.trash", "mail.messages.delete", "mail.attachments.get", "mail.drafts.get", "mail.drafts.edit", "mail.drafts.delete", "mail.drafts.send", "mail.reply", "mail.forward":
		fs.StringVar(&o.id, "id", "", "opaque message ID (required)")
	}
	switch c.Name {
	case "mail.messages.mark":
		fs.BoolVar(&o.seen, "seen", false, "assign Seen explicitly: --seen=true or --seen=false")
		fs.BoolVar(&o.flagged, "flagged", false, "assign Flagged explicitly: --flagged=true or --flagged=false")
	case "mail.messages.move", "mail.messages.trash":
		fs.StringVar(&o.destination, "destination", "", "exact destination mailbox; trash discovers special-use when omitted")
	case "mail.attachments.get":
		fs.IntVar(&o.attachment, "attachment", -1, "zero-based attachment index from message metadata (required)")
		fs.StringVar(&o.output, "output", "", "explicit output filename; never overwrites (required)")
	case "mail.drafts.create":
		fs.StringVar(&o.destination, "mailbox", "", "draft mailbox; discover special-use when omitted")
	}
	switch c.Name {
	case "mail.send", "mail.drafts.create", "mail.drafts.edit":
		fs.Var(&o.to, "to", "recipient email; repeat for multiple recipients")
		fs.Var(&o.cc, "cc", "CC recipient; repeat")
		fs.Var(&o.bcc, "bcc", "BCC recipient; repeat")
		fs.StringVar(&o.compose.Subject, "subject", "", "message subject")
		fs.StringVar(&o.compose.Text, "body", "", "plain text body (literal text)")
		fs.StringVar(&o.compose.HTML, "html", "", "HTML body (literal inert text)")
		fs.Var(&o.attachments, "attach", "attachment file; repeat; bounded local reads")
	case "mail.reply":
		fs.StringVar(&o.compose.Text, "body", "", "reply text")
		fs.BoolVar(&o.replyAll, "all", false, "reply to original sender and other recipients")
		fs.Var(&o.attachments, "attach", "attachment file; repeat")
	case "mail.forward":
		fs.Var(&o.to, "to", "forward recipient; repeat")
		fs.StringVar(&o.compose.Text, "body", "", "introductory text")
		fs.Var(&o.attachments, "attach", "attachment file; repeat")
	}
	return fs, o
}

func runMail(ctx context.Context, args []string, g globals, p policy.Policy, out, stderr io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		return printHelp(out, p, "mail")
	}
	if (args[0] == "folders" || args[0] == "messages" || args[0] == "attachments" || args[0] == "drafts") && (len(args) == 1 || (len(args) == 2 && (args[1] == "--help" || args[1] == "-h"))) {
		return printHelp(out, p, "mail."+args[0])
	}
	name, flagStart := "", 2
	if args[0] == "send" || args[0] == "reply" || args[0] == "forward" {
		name = "mail." + args[0]
		flagStart = 1
	} else {
		if len(args) < 2 {
			return fault.New("usage", "expected a mail command (see mail --help)")
		}
		name = "mail." + args[0] + "." + args[1]
	}
	c, err := policy.Resolve(name)
	if err != nil {
		return fault.Usage(err)
	}
	if err := p.Check(c.Name); err != nil {
		return err
	}
	fs, o := mailFlags(c, stderr)
	if err := fs.Parse(args[flagStart:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fault.Usage(err)
	}
	if fs.NArg() != 0 {
		return fault.New("usage", "unexpected mail positional arguments")
	}
	if c.Capability != policy.Read || c.Name == "mail.attachments.get" || c.Name == "mail.drafts.get" {
		return runMailAction(ctx, c, o, fs, g, p, out, stderr)
	}
	explicitMailbox := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "mailbox" {
			explicitMailbox = true
		}
	})
	if explicitMailbox && o.list.Mailbox == "" {
		return fault.New("usage", "--mailbox cannot be empty")
	}

	if g.force || g.dryRun {
		return fault.New("usage", "--force and --dry-run do not apply to read-only mail commands")
	}
	if c.Name == "mail.messages.get" && o.id == "" {
		return fault.New("usage", "--id is required")
	}
	if c.Name == "mail.messages.list" || c.Name == "mail.messages.search" {
		if o.list.Limit < 1 || o.list.Limit > 100 {
			return fault.New("usage", "--limit must be between 1 and 100")
		}
	}
	account, err := configuredAccount(&g)
	if err != nil {
		return err
	}
	settings, err := mailSettings(g, account)
	if err != nil {
		return err
	}
	if o.list.Mailbox == "" && o.list.Cursor == "" {
		o.list.Mailbox = settings.DefaultMailbox
	}
	switch c.Name {
	case "mail.messages.list", "mail.messages.search":
		if err := o.list.Validate(); err != nil {
			return err
		}
	case "mail.messages.get":
		if err := mail.ValidateID(o.id); err != nil {
			return err
		}
	}
	opts, err := mailCredentials(g, account, p.NoInput(), stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	client, err := mail.Dial(ctx, opts)
	if err != nil {
		return err
	}
	defer client.Close()
	switch c.Name {
	case "mail.folders.list":
		folders, err := client.Folders(ctx)
		if err != nil {
			return err
		}
		return printMailFolders(out, folders, g)
	case "mail.messages.get":
		message, err := client.Get(ctx, o.id)
		if err != nil {
			return err
		}
		return printMailMessage(out, message, g)
	default:
		page, err := client.List(ctx, o.list)
		if err != nil {
			return err
		}
		return printMailPage(out, stderr, page, g)
	}
}
