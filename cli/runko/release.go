// Release verbs (§14.10.3, §28.3 stage 17b): create, list - thin wrappers
// over runkod's /api/projects/{name}/releases endpoints via the same
// apiJSON plumbing every other daemon-backed verb rides. Releases are
// immutable: there is no edit/delete verb here or anywhere.
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
	"time"
)

// ReleaseInfo mirrors runkod's release wire shape (release.go's
// releaseWire, itself the webhook's release block plus created_at).
type ReleaseInfo struct {
	Project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"project"`
	Version       string    `json:"version"`
	TagRef        string    `json:"tag_ref"`
	TagSHA        string    `json:"tag_sha"`
	TargetSHA     string    `json:"target_sha"`
	HeadChangeKey string    `json:"head_change_key,omitempty"`
	Changelog     string    `json:"changelog,omitempty"`
	CreatedBy     string    `json:"created_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// CreateRelease posts POST /api/projects/{name}/releases.
func CreateRelease(ctx context.Context, client *http.Client, runkodURL, token, project, version string) (ReleaseInfo, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/projects/" + url.PathEscape(project) + "/releases"
	body := map[string]any{}
	if version != "" {
		body["version"] = version
	}
	var release ReleaseInfo
	if err := apiJSON(ctx, client, http.MethodPost, endpoint, token, body, &release); err != nil {
		return ReleaseInfo{}, err
	}
	return release, nil
}

// ListReleases fetches GET /api/projects/{name}/releases.
func ListReleases(ctx context.Context, client *http.Client, runkodURL, token, project string) ([]ReleaseInfo, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/projects/" + url.PathEscape(project) + "/releases"
	var page struct {
		Releases []ReleaseInfo `json:"releases"`
	}
	if err := apiJSON(ctx, client, http.MethodGet, endpoint, token, nil, &page); err != nil {
		return nil, err
	}
	return page.Releases, nil
}

func cmdRelease(args []string) error {
	if len(args) < 1 || (args[0] != "create" && args[0] != "list") {
		return usageError("usage: runko release create|list ... (see docs/cli-contract.md)")
	}
	switch args[0] {
	case "create":
		return cmdReleaseCreate(args[1:])
	default:
		return cmdReleaseList(args[1:])
	}
}

func cmdReleaseCreate(args []string) error {
	fs := flag.NewFlagSet("release create", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	project := fs.String("project", "", "project name (required)")
	version := fs.String("version", "", "explicit version (default: patch-bump the latest release; required for manual-versioning projects)")
	jsonOut := fs.Bool("json", false, "emit the created release as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return fmt.Errorf("release create: --project is required")
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	release, err := CreateRelease(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), *project, *version)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(release)
	}
	fmt.Printf("released %s %s\n  tag %s -> %.12s\n", release.Project.Name, release.Version, release.TagRef, release.TargetSHA)
	if release.Changelog != "" {
		fmt.Println(release.Changelog)
	}
	return nil
}

func cmdReleaseList(args []string) error {
	fs := flag.NewFlagSet("release list", flag.ExitOnError)
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	project := fs.String("project", "", "project name (required)")
	jsonOut := fs.Bool("json", false, "emit the release list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return fmt.Errorf("release list: --project is required")
	}
	cred, err := resolveCredential(*runkodURL, *token)
	if err != nil {
		return err
	}
	releases, err := ListReleases(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), *project)
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(releases)
	}
	if len(releases) == 0 {
		fmt.Printf("%s: no releases\n", *project)
		return nil
	}
	for _, r := range releases {
		by := r.CreatedBy
		if by == "" {
			by = "operator"
		}
		fmt.Printf("%s  %s  %.12s  by %s  %s\n", r.Version, r.TagRef, r.TargetSHA, by, r.CreatedAt.Format("2006-01-02"))
	}
	return nil
}
