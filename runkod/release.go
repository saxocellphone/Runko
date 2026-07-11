// Releases (§14.10.3, stage 17b): REST handlers, version/changelog
// derivation, and the release.created webhook builder. The decision core
// (createReleaseCore) lives in actions.go beside its siblings; rpc.go is
// the Connect encoder over the same core.
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/project"
)

// releaseWire is the one wire shape a Release has on REST (and, field for
// field, the webhook's release block plus created_at) - the single-contract
// rule.
type releaseWire struct {
	Project       checks.ReleaseWebhookProject `json:"project"`
	Version       string                       `json:"version"`
	TagRef        string                       `json:"tag_ref"`
	TagSHA        string                       `json:"tag_sha"`
	TargetSHA     string                       `json:"target_sha"`
	HeadChangeKey string                       `json:"head_change_key,omitempty"`
	Changelog     string                       `json:"changelog,omitempty"`
	CreatedBy     string                       `json:"created_by,omitempty"`
	CreatedAt     time.Time                    `json:"created_at"`
}

func releaseToWire(r Release) releaseWire {
	return releaseWire{
		Project:       checks.ReleaseWebhookProject{ID: r.ProjectName, Name: r.ProjectName, Path: r.ProjectPath},
		Version:       r.Version,
		TagRef:        r.TagRef,
		TagSHA:        r.TagSHA,
		TargetSHA:     r.TargetSHA,
		HeadChangeKey: r.HeadChangeKey,
		Changelog:     r.Changelog,
		CreatedBy:     r.CreatedBy,
		CreatedAt:     r.CreatedAt.UTC(),
	}
}

func (s *Server) handleCreateRelease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // empty body = auto version
	}
	release, apiErr := s.createReleaseCore(r.Context(), r.PathValue("name"), req.Version, s.principalFor(r), s.laneFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, releaseToWire(release))
}

func (s *Server) handleListReleases(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryInt(w, r, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(w, r, "offset")
	if !ok {
		return
	}
	releases, apiErr := s.listReleasesCore(r.Context(), r.PathValue("name"), limit, offset)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	out := make([]releaseWire, len(releases))
	for i, rel := range releases {
		out[i] = releaseToWire(rel)
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": out})
}

// resolveReleaseProject finds projectName at the trunk tip and reads its
// full manifest (index.Scan drops capability_config, so the release config
// comes from the PROJECT.yaml blob itself - tree-as-truth, §10.3).
func (s *Server) resolveReleaseProject(name string) (index.IndexedProject, project.ReleaseConfig, core.Revision, *apiError) {
	gstore := gitstore.New(s.RepoDir)
	trunkTip, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		return index.IndexedProject{}, project.ReleaseConfig{}, "", typedErr(http.StatusConflict, clierr.Error{
			Code: "trunk_unborn", Field: "monorepo",
			Message: fmt.Sprintf("trunk %s has no commits yet - nothing to release", s.TrunkRef),
		})
	}
	indexed, err := index.Scan(gstore, trunkTip, nil)
	if err != nil {
		return index.IndexedProject{}, project.ReleaseConfig{}, "", internalErr(err)
	}
	var found *index.IndexedProject
	for i := range indexed {
		if indexed[i].Name == name {
			found = &indexed[i]
			break
		}
	}
	if found == nil {
		return index.IndexedProject{}, project.ReleaseConfig{}, "", plainErr(http.StatusNotFound, fmt.Sprintf("no project named %q at trunk", name))
	}
	blob, err := gstore.GetBlob(trunkTip, path.Join(found.Path, "PROJECT.yaml"))
	if err != nil {
		return index.IndexedProject{}, project.ReleaseConfig{}, "", internalErr(err)
	}
	var manifest project.Manifest
	if err := yaml.Unmarshal(blob.Content, &manifest); err != nil {
		return index.IndexedProject{}, project.ReleaseConfig{}, "", internalErr(err)
	}
	cfg, enabled := manifest.ReleaseConfig(found.Path)
	if !enabled {
		return index.IndexedProject{}, project.ReleaseConfig{}, "", typedErr(http.StatusConflict, clierr.Error{
			Code: "release_not_enabled", Field: "project",
			Message:    fmt.Sprintf("project %q does not declare the release capability", name),
			Suggestion: "add `release` to the project's capabilities (and optionally capability_config.release) - absent means no release surface (§14.10.3)",
			DocURL:     "docs/design.md#14103-tags-and-releases-decided-2026-07-10-resolves-the-223-tag-governance-question",
		})
	}
	return *found, cfg, trunkTip, nil
}

func (s *Server) listReleasesCore(ctx context.Context, name string, limit, offset int) ([]Release, *apiError) {
	if _, _, _, apiErr := s.resolveReleaseProject(name); apiErr != nil {
		// A never-released but release-enabled project lists as empty; an
		// unknown or non-release project is the caller's error.
		return nil, apiErr
	}
	releases, err := s.Store.ListReleases(ctx, name, limit, offset)
	if err != nil {
		return nil, internalErr(err)
	}
	return releases, nil
}

var semverPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// nextVersion resolves the release version (§14.10.3): explicit wins
// (semver-validated unless versioning is manual), otherwise a patch bump
// of the latest release, otherwise the 0.1.0 first release.
func nextVersion(explicit string, cfg project.ReleaseConfig, latest Release, hasLatest bool) (string, *apiError) {
	if explicit != "" {
		if cfg.Versioning == "semver" && !semverPattern.MatchString(explicit) {
			return "", typedErr(http.StatusBadRequest, clierr.Error{
				Code: "invalid_version", Field: "version",
				Message:    fmt.Sprintf("%q is not x.y.z semver", explicit),
				Suggestion: `pass a semver version, or set capability_config.release.versioning: manual for free-form versions`,
			})
		}
		return explicit, nil
	}
	if cfg.Versioning == "manual" {
		return "", typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "version",
			Message:    "this project uses manual versioning - an explicit version is required",
			Suggestion: `pass --version <v>`,
		})
	}
	if !hasLatest {
		return "0.1.0", nil
	}
	m := semverPattern.FindStringSubmatch(latest.Version)
	if m == nil {
		return "", typedErr(http.StatusConflict, clierr.Error{
			Code: "invalid_version", Field: "version",
			Message:    fmt.Sprintf("latest release %q is not semver - cannot auto-bump", latest.Version),
			Suggestion: "pass --version explicitly",
		})
	}
	patch, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%s.%s.%d", m[1], m[2], patch+1), nil
}

// commitsSince lists commits in since..rev touching pathPrefix, newest
// first, capped - the changelog window. since "" means the path's whole
// history (a first release). Reuses the repo browser's trailer-extracting
// log format (history.go).
func (s *Server) commitsSince(rev core.Revision, since, pathPrefix string, limit int) ([]commitInfo, error) {
	args := []string{"log", "--format=" + logFormat, "--max-count=" + strconv.Itoa(limit)}
	if since != "" {
		args = append(args, since+".."+string(rev))
	} else {
		args = append(args, string(rev))
	}
	if pathPrefix != "" {
		args = append(args, "--", pathPrefix)
	}
	out, err := s.gitOut(args...)
	if err != nil {
		return nil, err
	}
	return parseLogRecords(out), nil
}

// changelogMaxCommits bounds the derivation window: a first release of a
// long-lived path must not render the whole repo history.
const changelogMaxCommits = 200

// deriveChangelog renders the from-changes changelog (§14.10.3): one line
// per commit since the previous release, naming the landed Change where
// the commit's Change-Id trailer resolves to one (we own Change identity -
// better provenance than commit-message scraping). Returns the markdown
// and the newest resolved Change key (head_change_key).
func (s *Server) deriveChangelog(ctx context.Context, version string, commits []commitInfo) (string, string) {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n", version)
	headChangeKey := ""
	for _, c := range commits {
		key := ""
		if c.ChangeID != "" {
			if change, ok, err := s.Store.GetChange(ctx, c.ChangeID); err == nil && ok && change.State == "landed" {
				key = c.ChangeID
				if headChangeKey == "" {
					headChangeKey = key
				}
			}
		}
		if key != "" {
			fmt.Fprintf(&b, "- %s (%s)\n", c.Subject, key)
		} else {
			fmt.Fprintf(&b, "- %s\n", c.Subject)
		}
	}
	return b.String(), headChangeKey
}

// enqueueReleaseWebhook emits release.created (§14.10.3) - the CD trigger
// §14.10.1's read-side recipes key on instead of tag-polling.
func (s *Server) enqueueReleaseWebhook(ctx context.Context, r Release) {
	actor := checks.WebhookActor{Type: "user", ID: r.CreatedBy}
	if r.CreatedBy == "" {
		actor.ID = "unknown"
	}
	hook := checks.ReleaseCreatedWebhook{
		SpecVersion: "1",
		DeliveryID:  r.ProjectName + "@release@" + r.Version,
		Type:        "release.created",
		OccurredAt:  s.clock(),
		OrgID:       s.SettingsOrg,
		Release: checks.ReleaseWebhookRelease{
			Project:       checks.ReleaseWebhookProject{ID: r.ProjectName, Name: r.ProjectName, Path: r.ProjectPath},
			Version:       r.Version,
			TagRef:        r.TagRef,
			TagSHA:        r.TagSHA,
			TargetSHA:     r.TargetSHA,
			HeadChangeKey: r.HeadChangeKey,
			Changelog:     r.Changelog,
			CreatedBy:     actor,
		},
		API: checks.ReleaseWebhookAPI{
			ReleaseURL: "/api/projects/" + r.ProjectName + "/releases",
		},
	}
	payload, err := json.Marshal(hook)
	if err != nil {
		log.Printf("runkod: %s %s: marshal release webhook: %v", r.ProjectName, r.Version, err)
		return
	}
	if _, err := s.Store.EnqueueWebhook(ctx, hook.Type, payload); err != nil {
		log.Printf("runkod: %s %s: enqueue release webhook: %v", r.ProjectName, r.Version, err)
	}
}
