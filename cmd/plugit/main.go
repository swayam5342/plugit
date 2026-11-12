// Command plugit turns a REST API spec into a curated, safe MCP server.
package main

import (
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
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		cli.BuildInfo{Version: version, Commit: commit, Date: date}))
}
