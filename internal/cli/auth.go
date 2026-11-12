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

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/secrets"
)

// maxSecretBytes caps a credential read from input.
const maxSecretBytes = 16 << 10

type keyringSetter interface {
	Set(service, key, value string) error
}

type authDeps struct {
	in     io.Reader // read when not a terminal (piped input, for scripts)
	out    io.Writer
	errOut io.Writer // prompts
	// readHidden reads one line from the terminal without echo. nil means
	// in is not a terminal.
	readHidden func() ([]byte, error)
	keyring    keyringSetter
}

func newAuthCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "auth <name>",
		Short: "Set or rotate an API's credential in the OS keyring (prompted, never echoed)",
		Long: `Store an API's credential in the OS keyring item named by auth.secret in
its plugit.yaml (keyring:service/key). The value is read without echo, or from
stdin when it is not a terminal. For env: secrets, set the variable in the MCP
client's env block instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			d := authDeps{in: cmd.InOrStdin(), out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(), keyring: secrets.OSKeyring{}}
			if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) { // #nosec G115 -- a file descriptor fits in int
				d.readHidden = func() ([]byte, error) { return term.ReadPassword(int(f.Fd())) } // #nosec G115 -- as above
			}
			return authAPI(p, args[0], d)
		},
	}
}

// authAPI prompts for an API's credential and stores it in the keyring item
// its config names. The value is never echoed or included in errors.
func authAPI(p config.Paths, name string, d authDeps) error {
	cfg, err := config.LoadAPI(p, name)
	if err != nil {
		return err
	}
	if cfg.Auth.Type == config.AuthNone {
		return fmt.Errorf("API %s has no credential (auth.type is none); set auth in its plugit.yaml first", name)
	}
	ref, err := secrets.ParseSecretRef(cfg.Auth.Secret)
	if err != nil {
		return fmt.Errorf("auth.secret: %w", err)
	}
	if ref.Scheme != secrets.Keyring {
		return fmt.Errorf("auth.secret is %s, so there is nothing to store: set %s in the MCP client's env block, or change auth.secret to keyring:plugit/%s", ref, ref.Var, name)
	}

	hint := ""
	if cfg.Auth.Type == config.AuthBasic {
		hint = " as user:password"
	}
	secret, err := readSecret(d, fmt.Sprintf("Credential for %s%s (input hidden): ", name, hint))
	if err != nil {
		return err
	}
	switch {
	case secret == "":
		return errors.New("empty credential; nothing stored")
	case strings.ContainsFunc(secret, isControl):
		return errors.New("the credential contains control characters; nothing stored")
	case cfg.Auth.Type == config.AuthBasic && !strings.Contains(secret, ":"):
		return errors.New("basic auth credentials look like user:password; nothing stored")
	}
	if err := d.keyring.Set(ref.Service, ref.Key, secret); err != nil {
		// Backend messages are not wrapped: they could quote the value.
		return fmt.Errorf("%w: could not store %s", secrets.ErrKeyring, ref)
	}
	_, err = fmt.Fprintf(d.out, "Stored the credential for %s in %s.\n", name, ref)
	return err
}

func readSecret(d authDeps, prompt string) (string, error) {
	if d.readHidden != nil {
		if _, err := io.WriteString(d.errOut, prompt); err != nil {
			return "", err
		}
		b, err := d.readHidden()
		_, _ = io.WriteString(d.errOut, "\n")
		if err != nil {
			return "", fmt.Errorf("reading the credential: %w", err)
		}
		if len(b) > maxSecretBytes {
			return "", fmt.Errorf("credential too long (over %d bytes)", maxSecretBytes)
		}
		return string(b), nil
	}
	line, err := bufio.NewReaderSize(io.LimitReader(d.in, maxSecretBytes+2), 4096).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading the credential: %w", err)
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if len(line) > maxSecretBytes {
		return "", fmt.Errorf("credential too long (over %d bytes)", maxSecretBytes)
	}
	return line, nil
}

func isControl(r rune) bool { return r < ' ' || r == 0x7f }
