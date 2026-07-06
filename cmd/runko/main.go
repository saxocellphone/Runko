// Command runko is the human/agent-facing CLI (docs/design.md §17.1): auth,
// project create/add-capability, workspace create/attach, change create/push,
// doctor, mcp serve. Subcommand wiring lands in a later session (§28.3 stage 9);
// this is bootstrap scaffolding only.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "runko: not yet implemented (see docs/design.md §17.1, §28.3 stage 9)")
	os.Exit(1)
}
