package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// agentPolicy mirrors receive.AgentPolicy's JSON wire shape. Kept local so the
// CLI stays a thin API client, not coupled to platform/receive.
type agentPolicy struct {
	RequireWorkspaceAffinity bool     `json:"require_workspace_affinity"`
	RequireDescription       bool     `json:"require_description"`
	MaxChangedFiles          int      `json:"max_changed_files"`
	MaxDiffBytes             int64    `json:"max_diff_bytes"`
	CanCreateProjects        bool     `json:"can_create_projects"`
	CanLandChanges           bool     `json:"can_land_changes"`
	CanModifyOwners          bool     `json:"can_modify_owners"`
	CanEnableCapabilities    []string `json:"can_enable_capabilities"`
	DenylistPaths            []string `json:"denylist_paths"`
}

type agentPolicyResp struct {
	Org        string      `json:"org"`
	Overridden bool        `json:"overridden"`
	Policy     agentPolicy `json:"policy"`
}

const workflowsDenyGlob = "**/.github/workflows/**"

// adminBaseAndOrg splits an org-mount credential URL into the deployment root
// (where /api/admin/* is served) and the org name: https://host/o/runko ->
// (https://host, runko). --org overrides the org, since one operator credential
// administers any org.
func adminBaseAndOrg(credURL, orgOverride string) (base, org string, err error) {
	u, err := url.Parse(strings.TrimSuffix(credURL, "/"))
	if err != nil {
		return "", "", fmt.Errorf("parse credential URL: %w", err)
	}
	org = orgOverride
	if i := strings.Index(u.Path, "/o/"); i >= 0 {
		name := strings.TrimPrefix(u.Path[i:], "/o/")
		if j := strings.IndexByte(name, '/'); j >= 0 {
			name = name[:j]
		}
		if org == "" {
			org = name
		}
		u.Path = u.Path[:i]
	}
	if org == "" {
		return "", "", usageError("no org in the credential URL; pass --org <name>")
	}
	return u.String(), org, nil
}

func agentPolicyURL(base, org string) string {
	return base + "/api/admin/orgs/" + url.PathEscape(org) + "/agent-policy"
}

func newOrgAgentPolicyCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent-policy",
		Short: "Read or set an org's agent policy (operator only)",
		Long: `The per-org agent policy governs what an org's AGENTS may do: which
paths they may write (denylist), whether they may edit OWNERS / PROJECT.yaml,
size caps, and whether they may land. Absent a policy an org uses the safe
default; only an operator may set one - never an agent.`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(newOrgAgentPolicyGetCmd(a), newOrgAgentPolicySetCmd(a), newOrgAgentPolicyResetCmd(a))
	return cmd
}

func newOrgAgentPolicyGetCmd(a *app) *cobra.Command {
	var (
		orgFlag string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show an org's effective agent policy",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			base, org, err := adminBaseAndOrg(cred.URL, orgFlag)
			if err != nil {
				return err
			}
			var resp agentPolicyResp
			if err := apiJSON(cmd.Context(), http.DefaultClient, http.MethodGet, agentPolicyURL(base, org), cred.AuthHeader(), nil, &resp); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(resp)
			}
			printAgentPolicy(resp)
			return nil
		},
	}
	cmd.Flags().StringVar(&orgFlag, "org", "", "org to read (default: the org in the credential URL)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the policy as JSON")
	return cmd
}

func newOrgAgentPolicySetCmd(a *app) *cobra.Command {
	var (
		orgFlag        string
		fromJSON       string
		allowAllPaths  bool
		allowWorkflows bool
		denylist       []string
		allowOwners    bool
		denyOwners     bool
		canLand        bool
		noCanLand      bool
		maxFiles       int
		maxDiffBytes   int64
		jsonOut        bool
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set an org's agent policy (read-modify-write; operator only)",
		Long: `Loosen or tighten an org's agent policy. --from-json reads a COMPLETE
policy (- for stdin). Otherwise the org's current effective policy is read and
only the flags you pass are applied, then the result is stored. Loosening the
.github/workflows denylist or enabling can-land prints a warning.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			base, org, err := adminBaseAndOrg(cred.URL, orgFlag)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			purl := agentPolicyURL(base, org)

			var pol agentPolicy
			if fromJSON != "" {
				data, err := readJSONSource(fromJSON)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(data, &pol); err != nil {
					return usageError(fmt.Sprintf("parse --from-json: %v", err))
				}
			} else {
				var cur agentPolicyResp
				if err := apiJSON(ctx, http.DefaultClient, http.MethodGet, purl, cred.AuthHeader(), nil, &cur); err != nil {
					return err
				}
				pol = cur.Policy
				f := cmd.Flags()
				if allowAllPaths {
					pol.DenylistPaths = nil
				}
				if f.Changed("denylist") {
					pol.DenylistPaths = denylist
				}
				if allowWorkflows {
					pol.DenylistPaths = removeGlob(pol.DenylistPaths, workflowsDenyGlob)
				}
				if allowOwners {
					pol.CanModifyOwners = true
				}
				if denyOwners {
					pol.CanModifyOwners = false
				}
				if canLand {
					pol.CanLandChanges = true
				}
				if noCanLand {
					pol.CanLandChanges = false
				}
				if f.Changed("max-files") {
					pol.MaxChangedFiles = maxFiles
				}
				if f.Changed("max-diff-bytes") {
					pol.MaxDiffBytes = maxDiffBytes
				}
			}

			var resp agentPolicyResp
			if err := apiJSON(ctx, http.DefaultClient, http.MethodPut, purl, cred.AuthHeader(), pol, &resp); err != nil {
				return err
			}
			warnAgentPolicy(org, resp.Policy)
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(resp)
			}
			fmt.Printf("set agent policy for org %s\n", org)
			printAgentPolicy(resp)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&orgFlag, "org", "", "org to set (default: the org in the credential URL)")
	f.StringVar(&fromJSON, "from-json", "", "set the WHOLE policy from a JSON file (- for stdin)")
	f.BoolVar(&allowAllPaths, "allow-all-paths", false, "clear the path denylist entirely")
	f.BoolVar(&allowWorkflows, "allow-workflows", false, "drop only the .github/workflows denylist")
	f.StringSliceVar(&denylist, "denylist", nil, "set the path denylist to these glob patterns")
	f.BoolVar(&allowOwners, "allow-owners", false, "let agents edit OWNERS / PROJECT.yaml")
	f.BoolVar(&denyOwners, "deny-owners", false, "forbid agents editing OWNERS / PROJECT.yaml")
	f.BoolVar(&canLand, "can-land", false, "let agents land their own changes")
	f.BoolVar(&noCanLand, "no-can-land", false, "forbid agents landing")
	f.IntVar(&maxFiles, "max-files", 0, "max changed files per agent change (0 = no cap)")
	f.Int64Var(&maxDiffBytes, "max-diff-bytes", 0, "max diff bytes per agent change (0 = no cap)")
	f.BoolVar(&jsonOut, "json", false, "emit the stored policy as JSON")
	return cmd
}

func newOrgAgentPolicyResetCmd(a *app) *cobra.Command {
	var orgFlag string
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Drop an org's override so the default policy applies",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			base, org, err := adminBaseAndOrg(cred.URL, orgFlag)
			if err != nil {
				return err
			}
			var resp agentPolicyResp
			if err := apiJSON(cmd.Context(), http.DefaultClient, http.MethodDelete, agentPolicyURL(base, org), cred.AuthHeader(), nil, &resp); err != nil {
				return err
			}
			fmt.Printf("reset agent policy for org %s to the default\n", org)
			return nil
		},
	}
	cmd.Flags().StringVar(&orgFlag, "org", "", "org to reset (default: the org in the credential URL)")
	return cmd
}

// warnAgentPolicy prints the two relaxations that carry outsized risk: an agent
// that can edit CI workflows can reach CI secrets once its change lands, and an
// agent that can land composes dangerously with editable owners.
func warnAgentPolicy(org string, pol agentPolicy) {
	if !containsGlob(pol.DenylistPaths, workflowsDenyGlob) {
		fmt.Fprintf(os.Stderr, "warning: agents in org %s may now edit .github/workflows/** - a landed workflow runs with CI secrets. Put sensitive secrets (e.g. the GitHub App key) behind a protected GitHub Environment with required reviewers first.\n", org)
	}
	if pol.CanLandChanges {
		fmt.Fprintf(os.Stderr, "warning: agents in org %s may now LAND their own changes. A human still approves, but with owners also editable this approaches self-service - enable it deliberately.\n", org)
	}
}

func printAgentPolicy(r agentPolicyResp) {
	src := "default (no override)"
	if r.Overridden {
		src = "operator override"
	}
	fmt.Printf("org %s agent policy (%s):\n", r.Org, src)
	fmt.Printf("  workspace affinity required: %v\n", r.Policy.RequireWorkspaceAffinity)
	fmt.Printf("  description required:        %v\n", r.Policy.RequireDescription)
	fmt.Printf("  max changed files:           %d\n", r.Policy.MaxChangedFiles)
	fmt.Printf("  max diff bytes:              %d\n", r.Policy.MaxDiffBytes)
	fmt.Printf("  can create projects:         %v\n", r.Policy.CanCreateProjects)
	fmt.Printf("  can land changes:            %v\n", r.Policy.CanLandChanges)
	fmt.Printf("  can modify owners:           %v\n", r.Policy.CanModifyOwners)
	fmt.Printf("  can enable capabilities:     %v\n", r.Policy.CanEnableCapabilities)
	fmt.Printf("  denylist paths:              %v\n", r.Policy.DenylistPaths)
}

func readJSONSource(src string) ([]byte, error) {
	if src == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(src)
}

func containsGlob(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}

func removeGlob(s []string, drop string) []string {
	out := s[:0:0]
	for _, x := range s {
		if x != drop {
			out = append(out, x)
		}
	}
	return out
}
