// Package agentsmd generates a monorepo's AGENTS.md (docs/design.md §8.8:
// "reference prompts / skill files ... generated per monorepo" - this is
// §28.3 stage 11's second half, alongside search/). Per §8.3's decided
// CLI-first direction, the generated file teaches the runko/runko-ci CLI as
// the primary agent interface: a command inventory, --json output
// contracts, the exit-code convention, and the §6.5 structured-error shape
// - the same content docs/cli-contract.md documents for humans, rendered as
// a compact skill file for an agent's context window.
//
// Generate is a pure function over the Commands table below, not a
// hand-written string blob: adding a CLI command means adding one entry to
// Commands, not hunting for prose to update by hand. §8.2's context-budget
// rule applies here too - design.md §28.2 caps a generated AGENTS.md at 150
// lines, and generate_test.go asserts that bound so growth doesn't
// silently regress it.
package agentsmd
