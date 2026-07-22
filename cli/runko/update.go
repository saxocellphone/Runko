// Self-update (§17.1; docs/cli-contract.md): `runko self-update` replaces
// the running binary with the rolling `cli-latest` GitHub release build -
// the release .github/workflows/release-images.yml republishes whenever a
// landing affects the CLI input set, and the source of truth for binary
// distribution. Identity is content, not a version string: the binary is
// up to date iff its sha256 matches the release's checksums.txt entry for
// this GOOS/GOARCH, so a from-source build simply reads as "not the
// release build" and gets replaced. The swap is atomic: download to a temp
// file in the binary's own directory (same filesystem), verify the
// checksum, rename over the running executable (Windows: rename the old
// binary aside first - a running .exe can be renamed but not overwritten).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/internal/clierr"
)

const (
	// defaultReleaseRepo publishes the rolling cli-latest release
	// (release-images.yml's cli-release job).
	defaultReleaseRepo = "saxocellphone/runko"
	releaseTag         = "cli-latest"
	githubAPIBase      = "https://api.github.com"
	goInstallFallback  = "go install github.com/saxocellphone/runko/cli/runko@latest"
)

// githubRelease is the subset of GitHub's release JSON self-update reads.
type githubRelease struct {
	Name            string `json:"name"`
	TargetCommitish string `json:"target_commitish"`
	Assets          []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// UpdateOutcome is `runko self-update`'s result and --json shape.
type UpdateOutcome struct {
	UpToDate      bool   // this binary's sha256 already matches the release asset
	Updated       bool   // the binary was replaced (always false with --check)
	ReleaseCommit string // the release's target commit on main
	Asset         string // the platform asset consulted, e.g. runko_linux_amd64
	Path          string // the executable examined/replaced
}

// updateConfig parameterizes SelfUpdate so tests can point it at an
// httptest server and a scratch "executable" instead of api.github.com and
// the running binary.
type updateConfig struct {
	client  *http.Client
	apiBase string // e.g. https://api.github.com
	repo    string // owner/name
	goos    string
	goarch  string
	exePath string
	check   bool // report only, install nothing
}

// SelfUpdate implements the command against a config. It never touches
// cfg.exePath unless the downloaded asset verified against the published
// checksum.
func SelfUpdate(ctx context.Context, cfg updateConfig) (UpdateOutcome, error) {
	release, err := fetchRelease(ctx, cfg)
	if err != nil {
		return UpdateOutcome{}, err
	}

	assetName := "runko_" + cfg.goos + "_" + cfg.goarch
	if cfg.goos == "windows" {
		assetName += ".exe"
	}
	outcome := UpdateOutcome{
		ReleaseCommit: release.TargetCommitish,
		Asset:         assetName,
		Path:          cfg.exePath,
	}

	assetURL, checksumsURL := "", ""
	for _, a := range release.Assets {
		switch a.Name {
		case assetName:
			assetURL = a.BrowserDownloadURL
		case "checksums.txt":
			checksumsURL = a.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		return UpdateOutcome{}, &clierr.Error{
			Code:       "unsupported_platform",
			Message:    fmt.Sprintf("release %s has no prebuilt %s binary", releaseTag, cfg.goos+"/"+cfg.goarch),
			Suggestion: "build from source instead: " + goInstallFallback,
		}
	}
	if checksumsURL == "" {
		return UpdateOutcome{}, &clierr.Error{
			Code:       "checksums_missing",
			Message:    fmt.Sprintf("release %s has no checksums.txt asset, so the download cannot be verified", releaseTag),
			Suggestion: "retry once the release-images run finishes republishing the release, or build from source: " + goInstallFallback,
		}
	}

	wantSum, err := fetchChecksum(ctx, cfg.client, checksumsURL, assetName)
	if err != nil {
		return UpdateOutcome{}, err
	}
	haveSum, err := fileSHA256(cfg.exePath)
	if err != nil {
		return UpdateOutcome{}, fmt.Errorf("hash %s: %w", cfg.exePath, err)
	}
	if haveSum == wantSum {
		outcome.UpToDate = true
		return outcome, nil
	}
	if cfg.check {
		return outcome, nil
	}

	if err := installAsset(ctx, cfg, assetURL, assetName, wantSum); err != nil {
		return UpdateOutcome{}, err
	}
	outcome.Updated = true
	return outcome, nil
}

func fetchRelease(ctx context.Context, cfg updateConfig) (githubRelease, error) {
	url := strings.TrimSuffix(cfg.apiBase, "/") + "/repos/" + cfg.repo + "/releases/tags/" + releaseTag
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "runko-cli")
	resp, err := cfg.client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("contact %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return githubRelease{}, &clierr.Error{
			Code:       "release_not_found",
			Message:    fmt.Sprintf("github.com/%s has no %s release", cfg.repo, releaseTag),
			Suggestion: "check --repo points at a repo publishing the rolling " + releaseTag + " release, or build from source: " + goInstallFallback,
		}
	}
	if resp.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("%s returned %d", url, resp.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("decode release %s: %w", releaseTag, err)
	}
	return release, nil
}

// fetchChecksum downloads checksums.txt (sha256sum format: "<hex>  <name>"
// per line) and returns the hex digest published for assetName.
func fetchChecksum(ctx context.Context, client *http.Client, url, assetName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "runko-cli")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download checksums.txt: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download checksums.txt: %s returned %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("download checksums.txt: %w", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == assetName {
			return fields[0], nil
		}
	}
	return "", &clierr.Error{
		Code:       "checksums_missing",
		Message:    fmt.Sprintf("checksums.txt in release %s has no entry for %s", releaseTag, assetName),
		Suggestion: "retry once the release-images run finishes republishing the release, or build from source: " + goInstallFallback,
	}
}

// installAsset downloads assetURL next to cfg.exePath, verifies it against
// wantSum, carries the old file mode over, and swaps it into place.
func installAsset(ctx context.Context, cfg updateConfig, assetURL, assetName, wantSum string) error {
	dir := filepath.Dir(cfg.exePath)
	tmp, err := os.CreateTemp(dir, ".runko-self-update-*")
	if err != nil {
		if os.IsPermission(err) {
			return notWritable(dir, err)
		}
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		tmp.Close()
		return err
	}
	req.Header.Set("User-Agent", "runko-cli")
	resp, err := cfg.client.Do(req)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("download %s: %w", assetName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return fmt.Errorf("download %s: %s returned %d", assetName, assetURL, resp.StatusCode)
	}

	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, hash), resp.Body)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", assetName, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != wantSum {
		return &clierr.Error{
			Code:       "checksum_mismatch",
			Message:    fmt.Sprintf("downloaded %s hashes to %.12s..., but checksums.txt publishes %.12s...", assetName, got, wantSum),
			Suggestion: "retry - a mismatch usually means the release was mid-republish; if it persists, build from source: " + goInstallFallback,
		}
	}

	// Preserve the installed binary's mode (it may be 0555 in a shared
	// prefix); fall back to executable-for-everyone for a vanished file.
	mode := os.FileMode(0o755)
	if info, statErr := os.Stat(cfg.exePath); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return swapExecutable(tmpPath, cfg.exePath, cfg.goos)
}

// swapExecutable moves the verified download into place. On POSIX systems
// rename over the live path is atomic; Windows refuses to overwrite a
// running .exe but allows renaming it, so the old binary is moved aside
// to <path>.old first (and restored if the swap then fails).
func swapExecutable(tmpPath, exePath, goos string) error {
	if goos != "windows" {
		if err := os.Rename(tmpPath, exePath); err != nil {
			if os.IsPermission(err) {
				return notWritable(filepath.Dir(exePath), err)
			}
			return err
		}
		return nil
	}
	old := exePath + ".old"
	os.Remove(old) // a leftover from a previous update; best-effort
	if err := os.Rename(exePath, old); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		os.Rename(old, exePath) // put the working binary back
		return err
	}
	// The old running image can't delete itself on Windows; leave the
	// .old file for the caller (reported by cmdSelfUpdate).
	return nil
}

func notWritable(dir string, cause error) error {
	return &clierr.Error{
		Code:       "not_writable",
		Field:      "path",
		Message:    fmt.Sprintf("cannot write to %s: %v", dir, cause),
		Suggestion: "re-run with write access to the install directory (e.g. sudo runko self-update), or reinstall somewhere writable: " + goInstallFallback,
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func newSelfUpdateCmd() *cobra.Command {
	var (
		repo           string
		check, jsonOut bool
	)
	cmd := &cobra.Command{
		Use:     "self-update",
		Aliases: []string{"update"}, // the verb people type first
		Short:   "Replace this binary with the latest release build",
		GroupID: "start",
		Long: `Replaces the running binary with the rolling ` + releaseTag + ` GitHub
release build (§17.1). Identity is content, not a version string: up
to date iff the binary's sha256 matches the release's checksums.txt
entry for this platform, so a from-source build reads as stale and is
replaced. The download is checksum-verified before an atomic swap.`,
		Example: `  runko self-update --check   # report only; exit 0 either way
  runko self-update`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("self-update: locate the running binary: %w", err)
			}
			// Follow a symlinked install (~/bin/runko -> .../runko) and replace the
			// target, not the link - otherwise the "update" would orphan the real
			// binary and turn the link into a regular file.
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}

			outcome, err := SelfUpdate(context.Background(), updateConfig{
				client:  http.DefaultClient,
				apiBase: githubAPIBase,
				repo:    repo,
				goos:    runtime.GOOS,
				goarch:  runtime.GOARCH,
				exePath: exe,
				check:   check,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(outcome)
			}
			switch {
			case outcome.UpToDate:
				fmt.Printf("already up to date: %s matches %s (main @ %.9s)\n", outcome.Path, releaseTag, outcome.ReleaseCommit)
			case check:
				fmt.Printf("update available: %s differs from %s (main @ %.9s)\n  -> runko self-update   # install it\n", outcome.Path, releaseTag, outcome.ReleaseCommit)
			default:
				fmt.Printf("updated %s to %s (main @ %.9s)\n", outcome.Path, releaseTag, outcome.ReleaseCommit)
				if runtime.GOOS == "windows" {
					fmt.Printf("note: the previous binary was left at %s.old (Windows keeps a running image locked); delete it at your leisure\n", outcome.Path)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", defaultReleaseRepo, "GitHub repo (owner/name) publishing the rolling "+releaseTag+" release")
	cmd.Flags().BoolVar(&check, "check", false, "report whether an update is available; install nothing")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the UpdateOutcome as JSON")
	return cmd
}
