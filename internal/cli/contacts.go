package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"i4dcli/internal/contacts"
	"i4dcli/internal/fault"
	"i4dcli/internal/policy"
)

type contactOptions struct {
	book, path, query                                 string
	limit                                             int
	name, given, family, organization, note, birthday string
	emails, phones, urls                              calendarNames
	clearEmails, clearPhones, clearURLs               bool
	changes                                           contacts.Changes
}

func contactFlags(c policy.Command, out io.Writer) (*flag.FlagSet, *contactOptions) {
	fs := flag.NewFlagSet("i4d "+strings.ReplaceAll(c.Name, ".", " "), flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() { fmt.Fprintf(out, "Usage: i4d %s\n", c.Usage); fs.PrintDefaults(); printGlobalHelp(out) }
	o := &contactOptions{}
	if c.Name != "addressbooks.list" {
		fs.StringVar(&o.book, "addressbook", "", "address book full name or path (required)")
	}
	switch c.Name {
	case "contacts.list", "contacts.search":
		fs.IntVar(&o.limit, "limit", 100, "maximum returned contacts (1–1000)")
	}
	if c.Name == "contacts.search" {
		fs.StringVar(&o.query, "query", "", "text to match in name, email, phone or organization (required)")
	}
	if c.Name == "contacts.get" || c.Name == "contacts.edit" || c.Name == "contacts.delete" {
		fs.StringVar(&o.path, "path", "", "contact resource path (required)")
	}
	if c.Name == "contacts.create" || c.Name == "contacts.edit" {
		fs.StringVar(&o.name, "name", "", "formatted display name (required for create)")
		fs.StringVar(&o.given, "given-name", "", "given name")
		fs.StringVar(&o.family, "family-name", "", "family name")
		fs.StringVar(&o.organization, "organization", "", "organization; empty clears")
		fs.StringVar(&o.note, "note", "", "note; empty clears")
		fs.StringVar(&o.birthday, "birthday", "", "YYYY-MM-DD; empty clears")
		fs.Var(&o.emails, "email", "email address; repeat to replace email list")
		fs.Var(&o.phones, "phone", "phone number; repeat to replace phone list")
		fs.Var(&o.urls, "url", "URL; repeat to replace URL list")
		fs.BoolVar(&o.clearEmails, "clear-emails", false, "remove all email fields")
		fs.BoolVar(&o.clearPhones, "clear-phones", false, "remove all phone fields")
		fs.BoolVar(&o.clearURLs, "clear-urls", false, "remove all URL fields")
	}
	return fs, o
}
func (o *contactOptions) supplied(fs *flag.FlagSet) error {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			o.changes.Name = &o.name
		case "given-name":
			o.changes.GivenName = &o.given
		case "family-name":
			o.changes.FamilyName = &o.family
		case "organization":
			o.changes.Organization = &o.organization
		case "note":
			o.changes.Note = &o.note
		case "birthday":
			o.changes.Birthday = &o.birthday
		}
	})
	for _, x := range []struct {
		clear  bool
		values calendarNames
		target **[]string
	}{{o.clearEmails, o.emails, &o.changes.Emails}, {o.clearPhones, o.phones, &o.changes.Phones}, {o.clearURLs, o.urls, &o.changes.URLs}} {
		if x.clear && len(x.values) > 0 {
			return fault.New("usage", "cannot combine values with their clear flag")
		}
		if x.clear || len(x.values) > 0 {
			values := append([]string{}, x.values...)
			*x.target = &values
		}
	}
	return nil
}
func runContacts(ctx context.Context, args []string, g globals, p policy.Policy, out, stderr io.Writer) error {
	if len(args) == 1 || (len(args) == 2 && (args[1] == "--help" || args[1] == "-h")) {
		return printHelp(out, p, args[0])
	}
	c, err := policy.Resolve(args[0] + "." + args[1])
	if err != nil {
		return fault.Usage(err)
	}
	if err := p.Check(c.Name); err != nil {
		return err
	}
	fs, o := contactFlags(c, stderr)
	if err := fs.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fault.Usage(err)
	}
	if fs.NArg() != 0 {
		return fault.New("usage", "unexpected contact positional arguments")
	}
	if err := o.supplied(fs); err != nil {
		return err
	}
	mutation := c.Capability != policy.Read
	if !mutation && (g.force || g.dryRun) {
		return fault.New("usage", "force and dry-run only apply to contact mutations")
	}
	if g.force && c.Capability != policy.Delete {
		return fault.New("usage", "force only authorizes contact deletion")
	}
	q := contacts.Query{AddressBook: o.book, Search: o.query, Limit: o.limit}
	if c.Name != "addressbooks.list" {
		if err := q.Validate(); err != nil {
			return err
		}
	}
	if c.Name == "contacts.search" && strings.TrimSpace(o.query) == "" {
		return fault.New("usage", "--query is required")
	}
	if (c.Name == "contacts.list" || c.Name == "contacts.search") && o.limit < 1 {
		return fault.New("usage", "contact limit must be 1–1000")
	}
	if c.Name == "contacts.get" && o.path == "" {
		return fault.New("usage", "--path is required")
	}
	m := contacts.Mutation{Action: strings.TrimPrefix(c.Name, "contacts."), AddressBook: o.book, Path: o.path, Changes: o.changes}
	if mutation {
		if err := m.Validate(); err != nil {
			return err
		}
		if g.dryRun {
			preview, err := contacts.Preview(m)
			if err != nil {
				return err
			}
			return printMailValue(out, preview, g)
		}
		if c.Capability == policy.Delete {
			if err := confirmMail(os.Stdin, stderr, "Delete contact "+o.path+" from "+o.book, p.NoInput(), g.force); err != nil {
				return err
			}
		}
	}
	if _, err := configuredAccount(&g); err != nil {
		return err
	}
	password, err := credentials(g.username, p.NoInput(), stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	client := contacts.NewClient(g.username, password, p.ReadOnly())
	defer client.Close()
	switch c.Name {
	case "addressbooks.list":
		books, err := client.AddressBooks(ctx)
		if err != nil {
			return err
		}
		if g.asJSON {
			return json.NewEncoder(out).Encode(books)
		}
		w, flush := tableWriter(out, g.plain)
		fmt.Fprintln(w, "NAME\tPATH\tDESCRIPTION")
		for _, b := range books {
			fmt.Fprintf(w, "%s\t%s\t%s\n", clean(b.Name), clean(b.Path), clean(b.Description))
		}
		return flush()
	case "contacts.list", "contacts.search":
		page, err := client.List(ctx, q)
		if err != nil {
			return err
		}
		if g.asJSON {
			return json.NewEncoder(out).Encode(page)
		}
		w, flush := tableWriter(out, g.plain)
		fmt.Fprintln(w, "NAME\tEMAILS\tPHONES\tORGANIZATION\tPATH")
		for _, c := range page.Contacts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", clean(c.Name), clean(strings.Join(c.Emails, ", ")), clean(strings.Join(c.Phones, ", ")), clean(c.Organization), clean(c.Path))
		}
		if err := flush(); err != nil {
			return err
		}
		if page.Truncated {
			_, err = fmt.Fprintln(stderr, "Contacts truncated; narrow --query or raise --limit.")
		}
		return err
	case "contacts.get":
		value, err := client.Get(ctx, o.book, o.path)
		if err != nil {
			return err
		}
		return printMailValue(out, value, g)
	default:
		value, err := client.Mutate(ctx, m)
		if err != nil {
			return err
		}
		return printMailValue(out, value, g)
	}
}
