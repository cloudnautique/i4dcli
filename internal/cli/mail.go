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
	if settings.Address == "" {
		return config.Mail{}, fault.New("config", "configure account mail.address, set ICLOUD_MAIL_ADDRESS, or pass --mail-address; the calendar username is not a mail identity")
	}
	if err := settings.Validate(); err != nil {
		return config.Mail{}, fault.New("config", "%s", err)
	}
	if settings.IMAPUsername == "" {
		settings.IMAPUsername = settings.Address
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
	list mail.ListOptions
	id   string
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
	case "mail.messages.get":
		fs.StringVar(&o.id, "id", "", "opaque message ID from list/search (required)")
	}
	return fs, o
}

func runMail(ctx context.Context, args []string, g globals, p policy.Policy, out, stderr io.Writer) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		return printHelp(out, p, "mail")
	}
	if (args[0] == "folders" || args[0] == "messages") && (len(args) == 1 || (len(args) == 2 && (args[1] == "--help" || args[1] == "-h"))) {
		return printHelp(out, p, "mail."+args[0])
	}
	if len(args) < 2 {
		return fault.New("usage", "expected mail folders list or mail messages list/get/search")
	}
	c, err := policy.Resolve("mail." + args[0] + "." + args[1])
	if err != nil {
		return fault.Usage(err)
	}
	if err := p.Check(c.Name); err != nil {
		return err
	}
	fs, o := mailFlags(c, stderr)
	if err := fs.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fault.Usage(err)
	}
	if fs.NArg() != 0 {
		return fault.New("usage", "unexpected mail positional arguments")
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
