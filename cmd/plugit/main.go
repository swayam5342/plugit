// Command plugit turns a REST API spec into a curated, safe MCP server.
package main

import (
	"os"
	"runtime/debug"

	"github.com/swayam5342/plugit/internal/cli"
)

// go build -ldflags "-X main.version=v0.1.0 -X main.commit=abc1234 -X main.date=2026-11-12T00:00:00Z"
var (
	version = ""
	commit  = ""
	date    = ""
)

func main() {
	info := cli.BuildInfo{Version: version, Commit: commit, Date: date}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info = info.Fill(bi)
	}
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, info))
}
