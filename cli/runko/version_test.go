package main

import (
	"strings"
	"testing"
)

// TestBuildIdentityString pins the one-line human form for the three
// stamping situations a binary can be in: checkout build, go-install
// build, and unstamped (test binaries outside any VCS checkout).
func TestBuildIdentityString(t *testing.T) {
	cases := []struct {
		name string
		id   BuildIdentity
		want string
	}{
		{"checkout build", BuildIdentity{
			Revision: "0123456789abcdef0123456789abcdef01234567",
			Time:     "2026-07-16T00:00:00Z", Go: "go1.24",
		}, "0123456789ab (2026-07-16T00:00:00Z, go1.24)"},
		{"dirty checkout", BuildIdentity{
			Revision: "0123456789abcdef0123456789abcdef01234567",
			Modified: true, Go: "go1.24",
		}, "0123456789ab+dirty (go1.24)"},
		{"go install build", BuildIdentity{
			Module: "v0.3.1", Go: "go1.24",
		}, "v0.3.1 (go1.24)"},
		{"unstamped", BuildIdentity{Go: "go1.24"}, "unstamped (go1.24)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.id.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildIdentityIsReadable: whatever this test binary was built from,
// buildIdentity must not panic and must always know its toolchain.
func TestBuildIdentityIsReadable(t *testing.T) {
	id := buildIdentity()
	if !strings.HasPrefix(id.Go, "go") {
		t.Fatalf("Go = %q, want a toolchain version", id.Go)
	}
}
