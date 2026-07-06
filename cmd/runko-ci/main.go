// Command runko-ci is the portable CI-facing CLI/image (docs/design.md §14.6):
// checkout-change, affected, report-check - the core that native plugins for
// GitHub Actions/Buildkite/etc. wrap (§14.7). Subcommand wiring lands in a later
// session (§28.3 stage 9); this is bootstrap scaffolding only.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "runko-ci: not yet implemented (see docs/design.md §14.6, §28.3 stage 9)")
	os.Exit(1)
}
