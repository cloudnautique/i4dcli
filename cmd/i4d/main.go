package main

import (
	"context"
	"fmt"
	"i4dcli/internal/cli"
	"io"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	return cli.Run(ctx, args, out, stderr)
}
