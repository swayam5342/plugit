// Package cli holds the thin cobra command handlers for the plugit binary.
// Handlers parse flags and delegate to the internal packages; they hold no
// business logic of their own.
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/isolate"
)

// Main runs plugit with args (without the program name) and returns the
// process exit code. The hidden isolate.ChildArg first argument turns the
// process into a spec-parse child (see package isolate); it is not a cobra
// command, so it never appears in help.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, info BuildInfo) int {
	if len(args) > 0 && args[0] == isolate.ChildArg {
		return isolate.ChildMain(stdin, stdout)
	}
	root := NewRootCmd(info)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(stderr, "plugit:", err)
		return 1
	}
	return 0
}

// BuildInfo describes the running binary. Values are injected at build time
// with -ldflags; empty fields fall back to development defaults.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func (b BuildInfo) withDefaults() BuildInfo {
	if b.Version == "" {
		b.Version = "dev"
	}
	if b.Commit == "" {
		b.Commit = "none"
	}
	if b.Date == "" {
		b.Date = "unknown"
	}
	return b
}

// NewRootCmd builds the plugit command tree.
func NewRootCmd(info BuildInfo) *cobra.Command {
	info = info.withDefaults()
	g := &globalFlags{}
	root := &cobra.Command{
		Use:           "plugit",
		Short:         "Turn any REST API into a curated, safe MCP server",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&g.configDir, "config-dir", "", "config root (default: the OS user config dir + /plugit)")
	root.AddCommand(newVersionCmd(info), newRunCmd(info), newServeCmd(g, info), newAuthCmd(g), newTryCmd(g, info),
		newInstallCmd(g), newUninstallCmd(g))
	return root
}

// globalFlags are the flags every command accepts.
type globalFlags struct {
	configDir string
}

func (g *globalFlags) paths() (config.Paths, error) {
	root, err := config.RootDir(g.configDir)
	if err != nil {
		return config.Paths{}, err
	}
	return config.NewPaths(root), nil
}

func newVersionCmd(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the plugit version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "plugit %s (commit %s, built %s)\n",
				info.Version, info.Commit, info.Date)
			return err
		},
	}
}
