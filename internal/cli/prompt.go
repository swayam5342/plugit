package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// terminalConfirm returns a yes/no prompt on the command's terminal, or nil
// when stdin isn't a terminal (callers then require --yes).
func terminalConfirm(cmd *cobra.Command) func(string) (bool, error) {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(int(in.Fd())) { // #nosec G115 -- a file descriptor fits in int
		return nil
	}
	return func(q string) (bool, error) { return askYesNo(in, cmd.ErrOrStderr(), q) }
}

func askYesNo(in io.Reader, out io.Writer, q string) (bool, error) {
	if _, err := fmt.Fprintf(out, "%s [y/N] ", q); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(io.LimitReader(in, 64)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}
