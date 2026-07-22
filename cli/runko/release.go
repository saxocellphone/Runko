// Release verbs (§14.10.3, §28.3 stage 17b): create, list - thin wrappers
// over runkod's /api/projects/{name}/releases endpoints via the same
// apiJSON plumbing every other daemon-backed verb rides. Releases are
// immutable: there is no edit/delete verb here or anywhere.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
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

func newReleaseCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "release",
		Short:   "Cut and list immutable releases",
		GroupID: "repo",
		Long: `Releases are immutable: a server-minted tag + changelog
derived from the landed changes touching the project since the
previous release. There is no edit or delete verb anywhere - a wrong
release is followed by a corrected one.`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(newReleaseCreateCmd(a), newReleaseListCmd(a))
	return cmd
}

func newReleaseCreateCmd(a *app) *cobra.Command {
	var (
		project, version string
		jsonOut          bool
	)
	cmd := &cobra.Command{
		Use:   "create --project <p>",
		Short: "Cut an immutable release with a derived changelog",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("release create: --project is required")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			release, err := CreateRelease(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), project, version)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(release)
			}
			fmt.Printf("released %s %s\n  tag %s -> %.12s\n", release.Project.Name, release.Version, release.TagRef, release.TargetSHA)
			if release.Changelog != "" {
				fmt.Println(release.Changelog)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project name (required)")
	cmd.Flags().StringVar(&version, "version", "", "explicit version (default: patch-bump the latest release; required for manual-versioning projects)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the created release as JSON")
	return cmd
}

func newReleaseListCmd(a *app) *cobra.Command {
	var (
		project string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list --project <p>",
		Short: "The project's releases, newest first",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("release list: --project is required")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			releases, err := ListReleases(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), project)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(releases)
			}
			if len(releases) == 0 {
				fmt.Printf("%s: no releases\n", project)
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
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project name (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the release list as JSON")
	return cmd
}
