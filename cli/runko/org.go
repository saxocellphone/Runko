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
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

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
		strings.TrimSuffix(cred.URL, "/")+"/api/orgs", cred.AuthHeader(),
		map[string]string{"name": name}, &info)
	return info, err
}

func ListOrgs(ctx context.Context, client *http.Client, cred Credential) ([]OrgInfo, error) {
	var out struct {
		Orgs []OrgInfo `json:"orgs"`
	}
	err := apiJSON(ctx, client, http.MethodGet,
		strings.TrimSuffix(cred.URL, "/")+"/api/orgs", cred.AuthHeader(), nil, &out)
	return out.Orgs, err
}

func AddOrgMember(ctx context.Context, client *http.Client, cred Credential, org, name, role string) error {
	body := map[string]string{"name": name}
	if role != "" {
		body["role"] = role
	}
	return apiJSON(ctx, client, http.MethodPost,
		strings.TrimSuffix(cred.URL, "/")+"/api/orgs/"+url.PathEscape(org)+"/members",
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

func cmdOrg(args []string) error {
	if len(args) < 1 {
		return usageError("usage: runko org create|list|add-member|bootstrap ... (see docs/cli-contract.md)")
	}
	ctx := context.Background()
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("org create", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		name := fs.String("name", "", "org name (lowercase letters, digits, dashes)")
		noSwitch := fs.Bool("no-switch", false, "keep the stored login pointed where it is (default: rebind it to the new org)")
		jsonOut := fs.Bool("json", false, "emit the created org as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return usageError("usage: runko org create --name <org> [--no-switch] [--runkod-url <url> --token <t>]")
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		info, err := CreateOrg(ctx, http.DefaultClient, cred, *name)
		if err != nil {
			return err
		}
		base := strings.TrimSuffix(cred.URL, "/")
		// Rebind the stored login to the new org (§6.10): creating an org
		// means working in it next, and the hub cloned the creating
		// account's credential into it (per-org accounts), so the stored
		// secret is already valid there - re-typing the password into a
		// second `auth login` taught nothing. Rebinding is verified by
		// whoami before it is saved; the pre-login scripting form
		// (explicit --token) stores nothing, so there is nothing to move.
		switched := false
		if *token == "" && !*noSwitch {
			orgCred := cred
			orgCred.URL = base + info.APIBase
			if _, _, err := whoami(ctx, http.DefaultClient, orgCred); err == nil {
				if _, err := saveCredential(orgCred); err == nil {
					switched = true
				}
			}
		}
		if *jsonOut {
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

	case "list":
		fs := flag.NewFlagSet("org list", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit orgs as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		orgs, err := ListOrgs(ctx, http.DefaultClient, cred)
		if err != nil {
			return err
		}
		if *jsonOut {
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

	case "add-member":
		fs := flag.NewFlagSet("org add-member", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		org := fs.String("org", "", "org name")
		name := fs.String("name", "", "account to add (they must have signed up)")
		role := fs.String("role", "member", "member or admin")
		jsonOut := fs.Bool("json", false, "emit {org, name, role} as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *org == "" || *name == "" {
			return usageError("usage: runko org add-member --org <org> --name <account> [--role member|admin]")
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		if err := AddOrgMember(ctx, http.DefaultClient, cred, *org, *name, *role); err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"org": *org, "name": *name, "role": *role})
		}
		fmt.Printf("added %s to %s as %s\n", *name, *org, *role)
		return nil

	case "bootstrap":
		fs := flag.NewFlagSet("org bootstrap", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL (point it at <host>/o/<org>)")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit {seeded_genesis, change_id, title} as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		out, err := BootstrapOrg(ctx, http.DefaultClient, cred)
		if err != nil {
			return err
		}
		if *jsonOut {
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
	}
	return usageError("usage: runko org create|list|add-member|bootstrap ...")
}
