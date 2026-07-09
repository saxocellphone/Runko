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

func cmdOrg(args []string) error {
	if len(args) < 1 {
		return usageError("usage: runko org create|list|add-member ... (see docs/cli-contract.md)")
	}
	ctx := context.Background()
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("org create", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		name := fs.String("name", "", "org name (lowercase letters, digits, dashes)")
		jsonOut := fs.Bool("json", false, "emit the created org as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return usageError("usage: runko org create --name <org> [--runkod-url <url> --token <t>]")
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		info, err := CreateOrg(ctx, http.DefaultClient, cred, *name)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(info)
		}
		base := strings.TrimSuffix(cred.URL, "/")
		fmt.Printf("created org %s\n  git remote: %s%s\n  API base:   %s%s\n  -> runko auth login --runkod-url %s%s   # work in this org\n",
			info.Name, base, info.GitURL, base, info.APIBase, base, info.APIBase)
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
	}
	return usageError("usage: runko org create|list|add-member ...")
}
