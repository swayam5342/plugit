package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/httpx"
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
	// For type oauth2: how to resolve the client secret, reach the
	// authorization server, and wait between device-flow polls. nil means
	// the real ones.
	resolver *secrets.Resolver
	doer     httpx.Doer
	sleep    func(context.Context, time.Duration) error
}

func newAuthCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "auth <name>",
		Short: "Set or rotate an API's credential in the OS keyring (prompted, never echoed)",
		Long: `Store an API's credential in the OS keyring item named by auth.secret in
its plugit.yaml (keyring:service/key). The value is read without echo, or from
stdin when it is not a terminal. For env: secrets, set the variable in the MCP
client's env block instead.

For auth.type oauth2: with flow client_credentials, store the client secret
(if it lives in the keyring) and check it by fetching a token; with flow
device_code, sign in in the browser and keep the refresh token in the
keyring (plugit/oauth-refresh-<name>).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := g.paths()
			if err != nil {
				return err
			}
			d := authDeps{in: cmd.InOrStdin(), out: cmd.OutOrStdout(), errOut: cmd.ErrOrStderr(), keyring: secrets.OSKeyring{}, resolver: secrets.NewResolver()}
			if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) { // #nosec G115 -- a file descriptor fits in int
				d.readHidden = func() ([]byte, error) { return term.ReadPassword(int(f.Fd())) } // #nosec G115 -- as above
			}
			return authAPI(cmd.Context(), p, args[0], d)
		},
	}
}

// authAPI prompts for an API's credential and stores it in the keyring item
// its config names. The value is never echoed or included in errors.
func authAPI(ctx context.Context, p config.Paths, name string, d authDeps) error {
	cfg, err := config.LoadAPI(p, name)
	if err != nil {
		return err
	}
	switch cfg.Auth.Type {
	case config.AuthNone:
		return fmt.Errorf("API %s has no credential (auth.type is none); set auth in its plugit.yaml first", name)
	case config.AuthOAuth2:
		return authOAuth(ctx, cfg, d)
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
	return storeSecret(d, cfg, ref, fmt.Sprintf("Credential for %s%s (input hidden): ", name, hint))
}

// storeSecret reads a secret without echo and stores it in ref's keyring
// item. The value is never echoed or included in errors.
func storeSecret(d authDeps, cfg *config.API, ref secrets.Ref, prompt string) error {
	name := cfg.Name
	secret, err := readSecret(d, prompt)
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
