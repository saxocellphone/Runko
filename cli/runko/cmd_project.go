// `runko project` - create/list/delete Projects (§10.1, §10.3, §13.1).
// Command wiring only; the intent pipeline lives in platform/project and
// project.go.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/project"
)

func newProjectCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "project",
		Short:   "Create, list, and delete Projects",
		GroupID: "repo",
		Long: `Projects are the unit of ownership, checks, and dependency edges - a
folder with a PROJECT.yaml manifest. Creation is intent-based:
name/type/owners in, generated manifest + scaffold out.`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(newProjectCreateCmd(), newProjectListCmd(a), newProjectDeleteCmd(a))
	return cmd
}

func newProjectCreateCmd() *cobra.Command {
	var (
		repoDir, name, projectType, lang, owners, path, template, capabilities, buildEngine, api string
		noTemplate, jsonOut                                                                      bool
	)
	cmd := &cobra.Command{
		Use:   "create --name <name> --type <type>",
		Short: "Create a project from an intent, on top of HEAD",
		Long: `Creates a project locally: PROJECT.yaml + scaffold from the
type/language template, committed on top of HEAD with a fresh Change-Id -
pushable as-is with no amend step. All flags, deliberately no positional
name argument.`,
		Example: `  runko project create --name checkout-api --type service --api grpc
  runko project create --name docs-site --type app --lang ts
  runko project create --name tooling --type library --no-template --lang zig`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("project create: --name is required\n  -> runko project create --name <name> --type <type>")
			}
			intent := project.Intent{
				Name:         name,
				Type:         projectType,
				Language:     lang,
				NoTemplate:   noTemplate,
				Path:         path,
				TemplateID:   template,
				Owners:       splitNonEmpty(owners),
				Capabilities: splitNonEmpty(capabilities),
				BuildEngine:  buildEngine,
				API:          api,
			}
			rev, changeID, err := CreateProject(repoDir, intent)
			if err != nil {
				return err
			}
			// The RESOLVED path (empty --path derives from the name, plan.go) -
			// reporting the raw flag printed "path": "" for the common case.
			outPath := intent.Path
			if outPath == "" {
				outPath = intent.Name
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{
					"name": intent.Name, "path": outPath, "rev": rev, "change_id": changeID,
				})
			}
			fmt.Printf("created project %s at %s (Change-Id: %s)\n", intent.Name, rev, changeID)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&repoDir, "repo", ".", "path to the local repo")
	fl.StringVar(&name, "name", "", "project name")
	fl.StringVar(&projectType, "type", "", "project type: library|service|app|job|other")
	fl.StringVar(&lang, "lang", "", "project language: go|python|ts|rust|java|cpp (default go); other values need --no-template")
	fl.BoolVar(&noTemplate, "no-template", false, "skip template scaffolding: PROJECT.yaml + README only, --lang recorded verbatim")
	fl.StringVar(&owners, "owners", "", "comma-separated owner refs, e.g. group:commerce-eng")
	fl.StringVar(&path, "path", "", "project path (default: derived from name)")
	fl.StringVar(&template, "template", "", "template id (default: type's default template)")
	fl.StringVar(&capabilities, "capabilities", "", "comma-separated capabilities, e.g. http,rpc")
	fl.StringVar(&buildEngine, "build-engine", "", "build scaffold: bazel|vite|none (default by language: ts -> vite, else bazel)")
	fl.StringVar(&api, "api", "", "contract surface: grpc|rest|none - required for --type service, optional for app, unavailable elsewhere")
	fl.BoolVar(&jsonOut, "json", false, "emit {name, path, rev, change_id} as JSON instead of a human summary")
	return cmd
}

// newProjectListCmd lists the trunk-tip project index (§10.3) - added so
// server-side errors like unknown_project can suggest a CLI command
// instead of a raw API URL (§8.3's CLI-first decision).
func newProjectListCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects indexed at trunk",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			var projects []index.IndexedProject
			if err := apiJSON(context.Background(), http.DefaultClient, http.MethodGet,
				strings.TrimRight(cred.URL, "/")+"/api/projects", cred.AuthHeader(), nil, &projects); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(projects)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			for _, p := range rootProjectFirst(projects) {
				owners := make([]string, len(p.Owners))
				for i, o := range p.Owners {
					owners[i] = o.Ref
				}
				// The root project's path column is empty, which read as a
				// missing field rather than the fact it is: this project IS the
				// repo root and owns every path no deeper manifest claims. Name
				// it, and hoist the row - it is not a peer of the rest.
				path := p.Path
				if isRootProject(p) {
					path = "(root)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Type, path, strings.Join(owners, ","))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the project list as JSON")
	return cmd
}

// newProjectDeleteCmd is create's dual (§13.1) - a server-calling verb,
// because the deletion plan needs the trunk-tip index for edge-stripping
// and a sparse local worktree may not even hold the project's files. The
// server authors an ordinary open Change; nothing reaches trunk until it
// lands through the normal gates.
func newProjectDeleteCmd(a *app) *cobra.Command {
	var (
		name    string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "delete --name <name>",
		Short: "Open a server-authored Change deleting a project",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("project delete: --name is required\n  -> runko project delete --name <name>")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			var out struct {
				ChangeID string `json:"change_id"`
				Title    string `json:"title"`
			}
			if err := apiJSON(context.Background(), http.DefaultClient, http.MethodPost,
				strings.TrimRight(cred.URL, "/")+"/api/projects/"+url.PathEscape(name)+"/delete", cred.AuthHeader(), nil, &out); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			fmt.Printf("delete of %s opened as change %s - it lands through the normal gates\n", name, out.ChangeID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "project to delete")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit {change_id, title} as JSON")
	return cmd
}

// isRootProject / rootProjectFirst: the repo-root project (§10.3) exists
// so root glue - go.mod, Makefile, .github/, scripts/ - resolves a merge
// policy instead of falling to the fail-closed unowned-path default, and
// it carries the repo-wide root_invalidation/prose rules. Rootness is the
// PATH, never the name (`repo` is this repo's convention, not a reserved
// word); both spellings of the root path count, matching the daemon's own
// rule (runkod/deleteproject.go, runkod/bootstraporg.go). JSON output is
// deliberately untouched - this is presentation, and --json is a contract.
func isRootProject(p index.IndexedProject) bool {
	return p.Path == "" || p.Path == "."
}

func rootProjectFirst(projects []index.IndexedProject) []index.IndexedProject {
	out := make([]index.IndexedProject, 0, len(projects))
	for _, p := range projects {
		if isRootProject(p) {
			out = append(out, p)
		}
	}
	for _, p := range projects {
		if !isRootProject(p) {
			out = append(out, p)
		}
	}
	return out
}

func splitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(csv, ",") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
