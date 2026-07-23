package main

import "testing"

// TestAdminBaseAndOrg pins the org-mount -> deployment-root split the admin CLI
// depends on: the admin API lives at the root, but the credential URL carries
// the /o/<org> mount.
func TestAdminBaseAndOrg(t *testing.T) {
	cases := []struct {
		url, override, wantBase, wantOrg string
		wantErr                          bool
	}{
		{"https://host/o/runko", "", "https://host", "runko", false},
		{"https://host/o/runko/", "", "https://host", "runko", false},
		{"https://host/o/runko", "acme", "https://host", "acme", false}, // --org overrides
		{"https://host:8080/o/team/extra", "", "https://host:8080", "team", false},
		{"https://host", "acme", "https://host", "acme", false}, // no /o/, --org supplies it
		{"https://host", "", "", "", true},                      // no org anywhere -> error
	}
	for _, c := range cases {
		base, org, err := adminBaseAndOrg(c.url, c.override)
		if (err != nil) != c.wantErr {
			t.Fatalf("%q/%q: err=%v wantErr=%v", c.url, c.override, err, c.wantErr)
		}
		if c.wantErr {
			continue
		}
		if base != c.wantBase || org != c.wantOrg {
			t.Fatalf("%q/%q: got (%q,%q) want (%q,%q)", c.url, c.override, base, org, c.wantBase, c.wantOrg)
		}
	}
}

func TestRemoveGlob(t *testing.T) {
	got := removeGlob([]string{"security/**", workflowsDenyGlob, "x/**"}, workflowsDenyGlob)
	if containsGlob(got, workflowsDenyGlob) {
		t.Fatalf("workflows glob not removed: %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("removeGlob dropped the wrong count: %v", got)
	}
}
