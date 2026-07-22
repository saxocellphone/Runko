package main

// The help surface is product copy, not an engineering record. Spec
// citations ("§13.5", "docs/design.md") mean nothing to someone reading
// `runko change land --help`, and design.md is a retired historical
// document - a pointer no reader can follow. The whole cobra tree is
// walked here so a new command cannot reintroduce them.

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	citemplates "github.com/saxocellphone/runko/templates/ci"
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

// `runko ci init` copies templates/ci verbatim into someone else's
// repository, where a design.md section number names a document that does
// not exist. Guarded here rather than beside the templates because the
// root project - which owns templates/ - declares no Go test lane.
func TestScaffoldedCITemplatesCarryNoSpecCitations(t *testing.T) {
	entries, err := fs.ReadDir(citemplates.FS, ".")
	if err != nil {
		t.Fatalf("read embedded CI templates: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded CI templates are empty - wrong FS?")
	}
	for _, entry := range entries {
		content, err := fs.ReadFile(citemplates.FS, entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		for i, line := range strings.Split(string(content), "\n") {
			for _, bad := range forbiddenInHelp {
				if strings.Contains(line, bad) {
					t.Errorf("templates/ci/%s:%d cites %q - scaffolded into other repos:\n  %s",
						entry.Name(), i+1, bad, strings.TrimSpace(line))
				}
			}
		}
	}
}
