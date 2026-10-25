package main

import (
	"context"
	"os"

	"github.com/dsolsona/almanac/internal/command"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	info := command.BuildInfo{SchemaVersion: 1, Version: version, Commit: commit, Date: date}
	os.Exit(command.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, info))
}
