// Org management (§7.1 multi-org, runkod/orghub.go): create an org (its
// own repo under /o/<name>/), list yours, add members. Thin wrappers over
// the hub's REST API using the same apiJSON plumbing as everything else.
//
// The org-scoped commands themselves need no org verb at all: point
// --runkod-url (or `runko auth login`) at <host>/o/<org> and every
// existing command - change, workspace, project, mcp - works against
// that org unchanged. These verbs exist for the hub-level surface only.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// hubBase strips a stored credential's /o/<org> mount suffix: the org
// verbs talk to the HUB (host root), but after signup/org-create the
// stored login deliberately points at the org mount - naively appending
// /api/orgs produced /o/<org>/api/orgs, a 404, the moment onboarding
// completed (found by the onboarding journey suite, 2026-07-17).
func hubBase(credURL string) string {
	u := strings.TrimSuffix(credURL, "/")
	if i := strings.LastIndex(u, "/o/"); i >= 0 && !strings.Contains(u[i+3:], "/") {
		return u[:i]
	}
	return u
}

// OrgInfo mirrors runkod's orgInfo wire shape.
type OrgInfo struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	APIBase string `json:"api_base"`
	GitURL  string `json:"git_url"`
	Default bool   `json:"default"`
}

func CreateOrg(ctx context.Context, client *http.Client, cred Credential, name string) (OrgInfo, error) {
	var info OrgInfo
	err := apiJSON(ctx, client, http.MethodPost,
		hubBase(cred.URL)+"/api/orgs", cred.AuthHeader(),
		map[string]string{"name": name}, &info)
	return info, err
}

func ListOrgs(ctx context.Context, client *http.Client, cred Credential) ([]OrgInfo, error) {
	var out struct {
		Orgs []OrgInfo `json:"orgs"`
	}
	err := apiJSON(ctx, client, http.MethodGet,
		hubBase(cred.URL)+"/api/orgs", cred.AuthHeader(), nil, &out)
	return out.Orgs, err
}

func AddOrgMember(ctx context.Context, client *http.Client, cred Credential, org, name, role string) error {
	body := map[string]string{"name": name}
	if role != "" {
		body["role"] = role
	}
	return apiJSON(ctx, client, http.MethodPost,
		hubBase(cred.URL)+"/api/orgs/"+url.PathEscape(org)+"/members",
		cred.AuthHeader(), body, nil)
}

// BootstrapOrg wires `runko org bootstrap` to POST /api/org/bootstrap
// (runkod/bootstraporg.go): the one-command governance retrofit for an
// ownerless org - a self-landable Change adding root OWNERS naming you
// (plus the root manifest when none exists), or a directly seeded genesis
// when trunk is unborn.
type BootstrapOrgResult struct {
	SeededGenesis bool   `json:"seeded_genesis"`
	ChangeID      string `json:"change_id"`
	Title         string `json:"title"`
}

func BootstrapOrg(ctx context.Context, client *http.Client, cred Credential) (BootstrapOrgResult, error) {
	var out BootstrapOrgResult
	err := apiJSON(ctx, client, http.MethodPost,
		strings.TrimSuffix(cred.URL, "/")+"/api/org/bootstrap", cred.AuthHeader(), nil, &out)
	return out, err
}

func newOrgCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "org",
		Short:   "Create and manage orgs",
		GroupID: "repo",
		Long: `Every org mounts the FULL surface (git, REST, RPC) under /o/<org>/,
so pointing the stored login at an org's mount makes every
other command work against it unchanged - these verbs exist for the
hub-level surface only.`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(newOrgCreateCmd(a), newOrgListCmd(a), newOrgAddMemberCmd(a), newOrgBootstrapCmd(a))
	return cmd
}

func newOrgCreateCmd(a *app) *cobra.Command {
	var (
		name              string
		noSwitch, jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "create --name <org>",
		Short: "Create a new org, genesis-seeded and ready to work in",
		Long: `Creates an org owning its own repo at /o/<org>/; the
caller becomes its admin. The new org is GENESIS-SEEDED (root
manifest, OWNERS naming you, AGENTS.md - trunk is never unborn) and
the stored login is rebound to the new org's mount.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return usageError("usage: runko org create --name <org> [--no-switch] [--runkod-url <url> --token <t>]")
			}
			ctx := cmd.Context()
			cred, err := a.credential()
			if err != nil {
				return err
			}
			info, err := CreateOrg(ctx, http.DefaultClient, cred, name)
			if err != nil {
				return err
			}
			base := hubBase(cred.URL)
			// Rebind the stored login to the new org (§6.10): creating an org
			// means working in it next, and the hub cloned the creating
			// account's credential into it (per-org accounts), so the stored
			// secret is already valid there - re-typing the password into a
			// second `auth login` taught nothing. Rebinding is verified by
			// whoami before it is saved; the pre-login scripting form
			// (explicit --token) stores nothing, so there is nothing to move.
			switched := false
			if a.token == "" && !noSwitch {
				orgCred := cred
				orgCred.URL = base + info.APIBase
				if _, _, err := whoami(ctx, http.DefaultClient, orgCred); err == nil {
					if _, err := saveCredential(orgCred); err == nil {
						switched = true
					}
				}
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(info)
			}
			fmt.Printf("created org %s\n  git remote: %s%s\n  API base:   %s%s\n",
				info.Name, base, info.GitURL, base, info.APIBase)
			if switched {
				fmt.Printf("  signed in:  stored login now points at %s%s\n", base, info.APIBase)
				fmt.Println("next:")
				fmt.Println("  runko workspace create --name <workstream> --project repo   # a checkout to work in (repo is genesis-seeded)")
			} else {
				fmt.Printf("  -> runko auth login --runkod-url %s%s   # work in this org\n", base, info.APIBase)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "org name (lowercase letters, digits, dashes)")
	cmd.Flags().BoolVar(&noSwitch, "no-switch", false, "keep the stored login pointed where it is (default: rebind it to the new org)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the created org as JSON")
	return cmd
}

func newOrgListCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Orgs you can reach (role + git URL)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			orgs, err := ListOrgs(cmd.Context(), http.DefaultClient, cred)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(orgs)
			}
			for _, o := range orgs {
				marker := ""
				if o.Default {
					marker = " (default)"
				}
				fmt.Printf("%-24s %-10s %s%s\n", o.Name, o.Role, o.GitURL, marker)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit orgs as JSON")
	return cmd
}

func newOrgAddMemberCmd(a *app) *cobra.Command {
	var (
		org, name, role string
		jsonOut         bool
	)
	cmd := &cobra.Command{
		Use:   "add-member --org <org> --name <account>",
		Short: "Grant an existing account access to an org",
		Long: `Org admins and operators only; the account must already have signed
up (membership is not an invitation system). Roles: member, admin,
releaser (may write tags and cut releases under tag policy).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if org == "" || name == "" {
				return usageError("usage: runko org add-member --org <org> --name <account> [--role member|admin]")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			if err := AddOrgMember(cmd.Context(), http.DefaultClient, cred, org, name, role); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"org": org, "name": name, "role": role})
			}
			fmt.Printf("added %s to %s as %s\n", name, org, role)
			return nil
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "org name")
	cmd.Flags().StringVar(&name, "name", "", "account to add (they must have signed up)")
	cmd.Flags().StringVar(&role, "role", "member", "member or admin")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit {org, name, role} as JSON")
	return cmd
}

func newOrgBootstrapCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Retrofit governance onto an ownerless org",
		Long: `The genesis retrofit for an org whose trunk resolves no owners
anywhere (born before genesis, or imported bare): opens a
server-authored Change adding root OWNERS naming you - landable by you
right now under uploader consent. An unborn trunk is genesis-seeded
directly instead.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			out, err := BootstrapOrg(cmd.Context(), http.DefaultClient, cred)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			if out.SeededGenesis {
				fmt.Println("trunk was unborn - genesis seeded directly: root manifest, OWNERS (you), AGENTS.md, agent skill, CONTRIBUTING.md")
				fmt.Println("next:")
				fmt.Println("  runko workspace create --name <workstream> --project repo   # a checkout to work in")
				return nil
			}
			fmt.Printf("opened %s (%q)\n", out.ChangeID, out.Title)
			fmt.Println("your push is your consent (uploader model), so it is landable by you right now:")
			fmt.Printf("  runko change land --change %s\n", out.ChangeID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit {seeded_genesis, change_id, title} as JSON")
	return cmd
}
