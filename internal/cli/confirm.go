package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
	"i4dcli/internal/app"
	"i4dcli/internal/fault"
)

func confirmDelete(in *os.File, out io.Writer, request app.Mutation, noInput, force bool) error {
	if force {
		return nil
	}
	if noInput || !term.IsTerminal(int(in.Fd())) {
		return fault.New("confirmation_required", "deletion requires confirmation; pass --force to authorize unattended deletion")
	}
	return readDeleteConfirmation(in, out, request)
}
func readDeleteConfirmation(in io.Reader, out io.Writer, request app.Mutation) error {
	fmt.Fprintf(out, "Delete event %s from calendar %s? Type yes: ", clean(request.Path), clean(request.Calendar))
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil || strings.TrimSpace(answer) != "yes" {
		return fault.New("cancelled", "deletion cancelled")
	}
	return nil
}
