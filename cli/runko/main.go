// Command runko is the human/agent-facing CLI (docs/design.md §17.1) -
// the primary interface for both (§8.3), with MCP as a thin adapter for
// clients that can't shell out. The command tree, help, completions, and
// flag parsing are cobra/pflag (root.go, the clig.dev redesign
// 2026-07-22); each subcommand file wires flags and output shaping over
// the platform libraries, and docs/cli-contract.md pins the output
// contract.
//
// Exit codes (docs/cli-contract.md): 0 success, 1 a recognized command
// failed (structured error printed to stderr), 2 usage error (unknown
// command/subcommand keyword, unparseable flags). usageError marks the
// exit-2 class; everything else exits 1.
package main

import (
	"errors"
	"fmt"
	"os"
)

// usageError marks an error as a usage problem (wrong/missing subcommand
// keyword, unparseable flags) rather than a recognized command failing at
// runtime - main() maps it to exit code 2, everything else to exit code 1.
// The empty usageError (errUsageShown, root.go) means help already went
// to stderr and there is nothing further to print.
type usageError string

func (e usageError) Error() string { return string(e) }

func main() {
	err := newRootCmd().Execute()
	if err == nil {
		return
	}
	var ue usageError
	if errors.As(err, &ue) {
		if ue != "" {
			fmt.Fprintln(os.Stderr, "runko: "+string(ue))
		}
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "runko: %v\n", err)
	os.Exit(1)
}
