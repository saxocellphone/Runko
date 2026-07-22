// runko version - which binary is this? (2026-07-16 dogfood review: "hard
// to report which CLI bit us when behavior differs" - version drift between
// cli-latest, self-built, and source was a recurring confusion with no verb
// to disambiguate it.) The identity comes entirely from the Go toolchain's
// own VCS stamping (runtime/debug.ReadBuildInfo): a checkout build carries
// vcs.revision/vcs.time/vcs.modified, a `go install module@version` build
// carries the module version, and NOTHING here needs ldflags - so the
// release workflow, `go build` on a laptop, and `go test` binaries all
// report truthfully with zero build-system cooperation.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// BuildIdentity is the wire shape of `runko version --json` (and the
// fields doctor reprints). Empty strings mean the toolchain did not stamp
// that field (e.g. a test binary built outside any VCS checkout).
type BuildIdentity struct {
	Revision string `json:"revision,omitempty"` // full vcs commit SHA
	Time     string `json:"time,omitempty"`     // vcs commit timestamp
	Modified bool   `json:"modified,omitempty"` // built from a dirty tree
	Module   string `json:"module,omitempty"`   // module version (go install builds)
	Go       string `json:"go"`                 // toolchain that built the binary
}

// buildIdentity reads the running binary's stamped identity.
func buildIdentity() BuildIdentity {
	id := BuildIdentity{Go: runtime.Version()}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return id
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		id.Module = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			id.Revision = s.Value
		case "vcs.time":
			id.Time = s.Value
		case "vcs.modified":
			id.Modified = s.Value == "true"
		}
	}
	return id
}

// String renders the one-line human form, e.g.
// "abc1234 (2026-07-16T00:00:00Z, go1.24.2)" or "unstamped (go1.24.2)".
func (id BuildIdentity) String() string {
	rev := id.Revision
	if len(rev) > 12 {
		rev = rev[:12]
	}
	switch {
	case rev == "" && id.Module != "":
		rev = id.Module
	case rev == "":
		rev = "unstamped"
	}
	if id.Modified {
		rev += "+dirty"
	}
	if id.Time != "" {
		return fmt.Sprintf("%s (%s, %s)", rev, id.Time, id.Go)
	}
	return fmt.Sprintf("%s (%s)", rev, id.Go)
}

func newVersionCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "version",
		Short:   "Which binary is this: revision, build time, toolchain",
		GroupID: "start",
		Long: `Prints this binary's identity from the Go toolchain's own VCS build
stamp - checkout builds carry the revision (+dirty), go install builds
the module version, an unstamped binary says so. -v/--version on the
root are aliases; doctor reprints the same line first.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := buildIdentity()
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(id)
			}
			fmt.Printf("runko %s\n", id)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit {revision, time, modified, module, go} as JSON")
	return cmd
}
