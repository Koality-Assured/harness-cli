package main

import (
	"fmt"
	"os"

	"github.com/Koality-Assured/harness-cli/internal/commands"
)

var (
	version = "0.3.1"
	commit  = "none"
	date    = "unknown"
)

func main() {
	commands.RootCmd.Version = fmt.Sprintf("%s (commit: %s, built: %s)", version, commit, date)

	if err := commands.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
