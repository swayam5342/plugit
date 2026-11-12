// Command plugit turns a REST API spec into a curated, safe MCP server.
package main

import (
	"fmt"
	"os"

	"github.com/swayam5342/plugit/internal/cli"
)

// Set at build time, e.g.
//
//	go build -ldflags "-X main.version=v0.1.0 -X main.commit=abc1234 -X main.date=2026-11-12T00:00:00Z"
var (
	version = ""
	commit  = ""
	date    = ""
)

func main() {
	root := cli.NewRootCmd(cli.BuildInfo{Version: version, Commit: commit, Date: date})
	if err := root.Execute(); err != nil {
		// Errors go to stderr: stdout is reserved for JSON-RPC in serve/run.
		fmt.Fprintln(os.Stderr, "plugit:", err)
		os.Exit(1)
	}
}
