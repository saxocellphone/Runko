package main

// `runkod serve --help` is what an operator reads while wiring a
// deployment. Spec citations ("§9.3", "docs/design.md") are an
// engineering record they cannot resolve - design.md is a retired
// historical document. Driven through the real binary so the flag
// definitions are covered wherever they live.

import (
	"os/exec"
	"strings"
	"testing"
)

func TestServeHelpCarriesNoSpecCitations(t *testing.T) {
	bin := buildRunkod(t)
	for _, args := range [][]string{{"--help"}, {"serve", "--help"}} {
		// --help exits non-zero on some flag paths; the text is what matters.
		out, _ := exec.Command(bin, args...).CombinedOutput()
		if len(out) == 0 {
			t.Fatalf("runkod %s printed nothing", strings.Join(args, " "))
		}
		for _, bad := range []string{"§", "design.md"} {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, bad) {
					t.Errorf("runkod %s: help cites %q - operator-facing copy:\n  %s",
						strings.Join(args, " "), bad, strings.TrimSpace(line))
				}
			}
		}
	}
}
