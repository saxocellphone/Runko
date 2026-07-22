package main

// runko-ci's help is read by whoever wires the executor; spec citations
// ("§14.9", "docs/design.md") are an engineering record they cannot
// follow. The sibling test in cli/runko guards the human CLI.

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

var forbiddenInHelp = []string{"§", "design.md"}

func walkCommands(cmd *cobra.Command, visit func(*cobra.Command)) {
	visit(cmd)
	for _, sub := range cmd.Commands() {
		walkCommands(sub, visit)
	}
}

func TestHelpTextCarriesNoSpecCitations(t *testing.T) {
	walkCommands(newRootCmd(), func(cmd *cobra.Command) {
		surfaces := map[string]string{
			"Short":   cmd.Short,
			"Long":    cmd.Long,
			"Example": cmd.Example,
			"flags":   cmd.LocalFlags().FlagUsages(),
		}
		for where, text := range surfaces {
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
