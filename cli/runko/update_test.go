package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

// releaseServer serves a minimal GitHub releases API: the cli-latest
// release metadata plus asset downloads for one linux/amd64 binary and its
// checksums.txt. publishedSum lets a test publish a checksum that does NOT
// match the served bytes (the mid-republish window).
func releaseServer(t *testing.T, binary []byte, publishedSum string) *httptest.Server {
	t.Helper()
	if publishedSum == "" {
		sum := sha256.Sum256(binary)
		publishedSum = hex.EncodeToString(sum[:])
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/test/repo/releases/tags/cli-latest":
			json.NewEncoder(w).Encode(map[string]any{
				"name":             "runko CLI (rolling, main @ deadbeef0)",
				"target_commitish": "deadbeef00000000000000000000000000000000",
				"assets": []map[string]string{
					{"name": "runko_linux_amd64", "browser_download_url": server.URL + "/dl/runko_linux_amd64"},
					{"name": "runko-ci_linux_amd64", "browser_download_url": server.URL + "/dl/runko-ci_linux_amd64"},
					{"name": "checksums.txt", "browser_download_url": server.URL + "/dl/checksums.txt"},
				},
			})
		case "/dl/runko_linux_amd64":
			w.Write(binary)
		case "/dl/checksums.txt":
			fmt.Fprintf(w, "%s  runko_linux_amd64\n%s  runko-ci_linux_amd64\n", publishedSum, publishedSum)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// fakeExe writes a stand-in "installed binary" and returns its path.
func fakeExe(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runko")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake exe: %v", err)
	}
	return path
}

func testConfig(server *httptest.Server, exePath string, check bool) updateConfig {
	return updateConfig{
		client:  http.DefaultClient,
		apiBase: server.URL,
		repo:    "test/repo",
		goos:    "linux",
		goarch:  "amd64",
		exePath: exePath,
		check:   check,
	}
}

func TestSelfUpdateReplacesTheBinary(t *testing.T) {
	server := releaseServer(t, []byte("release build v2"), "")
	exe := fakeExe(t, "stale local build")

	outcome, err := SelfUpdate(context.Background(), testConfig(server, exe, false))
	if err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	if !outcome.Updated || outcome.UpToDate {
		t.Fatalf("expected an update, got %+v", outcome)
	}
	if outcome.Asset != "runko_linux_amd64" || outcome.ReleaseCommit != "deadbeef00000000000000000000000000000000" {
		t.Fatalf("outcome misreports the release: %+v", outcome)
	}
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != "release build v2" {
		t.Fatalf("expected the binary replaced with the release bytes, got %q (err %v)", got, err)
	}
	info, err := os.Stat(exe)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("expected the old 0755 mode preserved, got %v (err %v)", info.Mode(), err)
	}
}

func TestSelfUpdateAlreadyUpToDate(t *testing.T) {
	server := releaseServer(t, []byte("release build v2"), "")
	exe := fakeExe(t, "release build v2")

	outcome, err := SelfUpdate(context.Background(), testConfig(server, exe, false))
	if err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	if !outcome.UpToDate || outcome.Updated {
		t.Fatalf("expected up-to-date and no install, got %+v", outcome)
	}
}

func TestSelfUpdateCheckReportsWithoutInstalling(t *testing.T) {
	server := releaseServer(t, []byte("release build v2"), "")
	exe := fakeExe(t, "stale local build")

	outcome, err := SelfUpdate(context.Background(), testConfig(server, exe, true))
	if err != nil {
		t.Fatalf("SelfUpdate: %v", err)
	}
	if outcome.UpToDate || outcome.Updated {
		t.Fatalf("expected update-available with nothing installed, got %+v", outcome)
	}
	if got, _ := os.ReadFile(exe); string(got) != "stale local build" {
		t.Fatalf("--check must not touch the binary, but it now reads %q", got)
	}
}

func TestSelfUpdateChecksumMismatchLeavesBinaryAlone(t *testing.T) {
	sum := sha256.Sum256([]byte("what the release SHOULD contain"))
	server := releaseServer(t, []byte("corrupted download"), hex.EncodeToString(sum[:]))
	exe := fakeExe(t, "stale local build")

	_, err := SelfUpdate(context.Background(), testConfig(server, exe, false))
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "checksum_mismatch" {
		t.Fatalf("expected checksum_mismatch, got %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "stale local build" {
		t.Fatalf("a failed verification must not touch the binary, but it now reads %q", got)
	}
	// The rejected download must not litter the install directory.
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".runko-self-update-*"))
	if len(leftovers) != 0 {
		t.Fatalf("expected the temp download removed, found %v", leftovers)
	}
}

func TestSelfUpdateUnsupportedPlatform(t *testing.T) {
	server := releaseServer(t, []byte("release build v2"), "")
	cfg := testConfig(server, fakeExe(t, "stale"), false)
	cfg.goarch = "riscv64"

	_, err := SelfUpdate(context.Background(), cfg)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "unsupported_platform" {
		t.Fatalf("expected unsupported_platform, got %v", err)
	}
}

func TestSelfUpdateReleaseNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer server.Close()

	_, err := SelfUpdate(context.Background(), testConfig(server, fakeExe(t, "stale"), false))
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "release_not_found" {
		t.Fatalf("expected release_not_found, got %v", err)
	}
}
