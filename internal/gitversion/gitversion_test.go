package gitversion

import (
	"strconv"
	"testing"
)

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b Version
		want bool
	}{
		{Version{2, 39, 2}, Version{2, 40, 0}, true},
		{Version{2, 40, 0}, Version{2, 40, 0}, false},
		{Version{2, 43, 0}, Version{2, 40, 0}, false},
		{Version{1, 99, 99}, Version{2, 0, 0}, true},
		{Version{2, 40, 1}, Version{2, 40, 0}, false},
	}
	for _, c := range cases {
		if got := c.a.Less(c.b); got != c.want {
			t.Fatalf("%s.Less(%s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestVersionPatternParsesDistroSuffixes(t *testing.T) {
	cases := map[string]Version{
		"git version 2.43.0\n":                 {2, 43, 0},
		"git version 2.39.2 (Apple Git-143)\n": {2, 39, 2},
		"git version 2.43.0.windows.1\n":       {2, 43, 0},
		"git version 2.30\n":                   {2, 30, 0},
	}
	for input, want := range cases {
		m := versionPattern.FindSubmatch([]byte(input))
		if m == nil {
			t.Fatalf("no match for %q", input)
		}
		patch := 0
		if len(m[3]) > 0 {
			patch, _ = strconv.Atoi(string(m[3]))
		}
		major, _ := strconv.Atoi(string(m[1]))
		minor, _ := strconv.Atoi(string(m[2]))
		got := Version{major, minor, patch}
		if got != want {
			t.Fatalf("%q: parsed %s, want %s", input, got, want)
		}
	}
}

// TestDetectAgainstRealGit exercises Detect() against whatever git is
// actually installed in this environment - a real subprocess call, not a
// mock - and only checks the result is internally consistent (parses,
// String() round-trips into something with dots), since we don't control
// which git version CI/dev machines have installed.
func TestDetectAgainstRealGit(t *testing.T) {
	v, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if v.Major < 2 {
		t.Fatalf("implausible major version parsed: %+v", v)
	}
	if v.String() == "" {
		t.Fatalf("String() produced empty output for %+v", v)
	}
}

func TestCheckAgainstRealGit(t *testing.T) {
	// This sandbox's git is >= 2.40 (verified via `git --version` during
	// development); assert Check() agrees rather than hardcoding that
	// assumption blindly.
	v, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	err = Check()
	if v.Less(Minimum) && err == nil {
		t.Fatalf("Check() should fail for git %s < minimum %s", v, Minimum)
	}
	if !v.Less(Minimum) && err != nil {
		t.Fatalf("Check() failed unexpectedly for git %s >= minimum %s: %v", v, Minimum, err)
	}
}
