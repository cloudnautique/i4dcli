package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/server"
	"i4dcli/internal/app"
	"i4dcli/internal/calendar"
	"i4dcli/internal/contacts"
	"i4dcli/internal/fault"
	"i4dcli/internal/mail"
	"i4dcli/internal/mcpserver"
	"i4dcli/internal/policy"
)

type mcpOptions struct {
	mcpserver.Options
	listTools bool
	tools     calendarNames
}

func mcpFlags(out io.Writer) (*flag.FlagSet, *mcpOptions) {
	o := &mcpOptions{}
	fs := flag.NewFlagSet("i4d mcp", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.BoolVar(&o.listTools, "list-tools", false, "print enabled tool schemas as JSON without credentials")
	fs.Var(&o.tools, "allow-tool", "exact tool names, comma-separated; may be repeated")
	fs.BoolVar(&o.AllowSend, "allow-send", false, "expose mail and calendar scheduling tools; requires allow-write and operator force")
	fs.BoolVar(&o.AllowWrite, "allow-write", false, "expose permitted create/edit tools")
	fs.BoolVar(&o.AllowDelete, "allow-delete", false, "expose delete tool; requires --allow-write and --force for execution")
	fs.IntVar(&o.MaxResults, "max-results", 1000, "maximum list results returned per call (1–10000)")
	fs.IntVar(&o.MaxOutputBytes, "max-output-bytes", 1<<20, "maximum JSON result bytes per call (1024–10485760)")
	fs.Usage = func() { fmt.Fprintln(out, "Usage: i4d mcp [options]"); fs.PrintDefaults(); printGlobalHelp(out) }
	return fs, o
}
func runMCP(ctx context.Context, args []string, g globals, p policy.Policy, out, stderr io.Writer) error {
	if err := p.Check("mcp"); err != nil {
		return err
	}
	fs, o := mcpFlags(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fault.Usage(err)
	}
	if fs.NArg() != 0 {
		return fault.New("usage", "unexpected MCP positional arguments")
	}
	if g.plain || (g.asJSON && !o.listTools) {
		return fault.New("usage", "MCP uses JSON-RPC on stdout; --json is only supported with --list-tools and --plain is not supported")
	}
	o.AllowTools = o.tools
	o.Timeout = g.timeout
	o.Force = g.force
	o.DryRun = g.dryRun
	if o.listTools {
		tools, err := mcpserver.Tools(p, o.Options)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(tools)
	}
	account, err := configuredAccount(&g)
	if err != nil {
		return err
	}
	o.DefaultTimezone = account.Timezone
	o.ContactsFactory = func(ctx context.Context) (mcpserver.ContactsClient, error) {
		password, err := credentials(g.username, true, io.Discard)
		if err != nil {
			return nil, err
		}
		return contacts.NewClient(g.username, password, p.ReadOnly() || !o.AllowWrite), nil
	}
	o.MailIdentity = func() (string, error) { settings, err := mailSettings(g, account); return settings.Address, err }
	o.MailSubmit = func(ctx context.Context, message mail.PreparedMessage) (mail.SendResult, error) {
		opts, err := mailSMTPOptions(g, account, true, io.Discard)
		if err != nil {
			return mail.SendResult{}, err
		}
		return mail.Send(ctx, opts, message)
	}
	o.MailFactory = func(ctx context.Context) (mcpserver.MailClient, error) {
		opts, err := mailCredentials(g, account, true, io.Discard)
		if err != nil {
			return nil, err
		}
		opts.AllowWrite = o.AllowWrite && !p.ReadOnly()
		return mail.Dial(ctx, opts)
	}
	factory := func(context.Context) (*app.Service, func(), error) {
		password, err := credentials(g.username, true, io.Discard)
		if err != nil {
			return nil, nil, err
		}
		client := calendar.NewClient(strings.TrimSpace(g.username), password)
		return app.New(client, calendar.Endpoint, p), client.CloseIdleConnections, nil
	}
	srv, err := mcpserver.New(p, o.Options, factory)
	if err != nil {
		return err
	}
	stdio := server.NewStdioServer(srv)
	stdio.SetErrorLogger(log.New(stderr, "mcp: ", 0))
	return stdio.Listen(ctx, os.Stdin, out)
}
