package main

// The help surface is product copy, not an engineering record. Spec
// citations ("§13.5", "docs/design.md") mean nothing to someone reading
// `runko change land --help`, and design.md is a retired historical
// document - a pointer no reader can follow. The whole cobra tree is
// walked here so a new command cannot reintroduce them.

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// forbiddenInHelp are substrings that mark internal-record language
// leaking into user-facing copy.
var forbiddenInHelp = []string{"§", "design.md"}

// walkCommands visits cmd and every descendant.
func walkCommands(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, sub := range cmd.Commands() {
		walkCommands(sub, visit)
	}
}

// helpSurfaces is everything `<cmd> --help` prints that we author:
// the descriptions, the examples, and the flag usage block.
func helpSurfaces(cmd *cobra.Command) map[string]string {
	return map[string]string{
		"Short":   cmd.Short,
		"Long":    cmd.Long,
		"Example": cmd.Example,
		"flags":   cmd.LocalFlags().FlagUsages(),
	}
}

func TestHelpTextCarriesNoSpecCitations(t *testing.T) {
	walkCommands(newRootCmd(), func(cmd *cobra.Command) {
		for where, text := range helpSurfaces(cmd) {
			for _, bad := range forbiddenInHelp {
				if !strings.Contains(text, bad) {
					continue
				}
				for _, line := range strings.Split(text, "\n") {
					if strings.Contains(line, bad) {
						t.Errorf("`%s` %s cites %q - help text is product copy:\n  %s",
							cmd.CommandPath(), where, bad, strings.TrimSpace(line))
					}
				}
			}
		}
	})
}
