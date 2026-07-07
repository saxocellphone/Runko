package search

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// scriptedZoektGitIndex writes an executable shell script standing in for
// zoekt-git-index, recording its full argv into recordPath - the same
// technique buildadapter/bazel/bazel_test.go's scriptedBazel and runkod/
// gitleaks_test.go's scriptedGitleaks use for a real binary this sandbox
// doesn't have.
func scriptedZoektGitIndex(t *testing.T, recordPath string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-zoekt-git-index")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > " + shellQuote(recordPath) + "\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zoekt-git-index script: %v", err)
	}
	return bin
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func TestZoektIndexerInvokesRealArgs(t *testing.T) {
	scratch := t.TempDir()
	record := filepath.Join(scratch, "argv.txt")
	bin := scriptedZoektGitIndex(t, record, 0)
	indexDir := filepath.Join(scratch, "index")

	indexer := ZoektIndexer{Bin: bin, IndexDir: indexDir}
	if err := indexer.Index(context.Background(), "/repo/bare.git"); err != nil {
		t.Fatalf("Index: %v", err)
	}

	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	argv := strings.TrimSpace(string(got))
	if !strings.Contains(argv, "-index "+indexDir) {
		t.Fatalf("expected -index %s in argv, got %q", indexDir, argv)
	}
	if !strings.Contains(argv, "-branches HEAD") {
		t.Fatalf("expected default -branches HEAD in argv, got %q", argv)
	}
	if !strings.HasSuffix(argv, "/repo/bare.git") {
		t.Fatalf("expected the repo dir as the final positional arg, got %q", argv)
	}
	if _, err := os.Stat(indexDir); err != nil {
		t.Fatalf("expected Index to create IndexDir up front: %v", err)
	}
}

func TestZoektIndexerCustomBranches(t *testing.T) {
	scratch := t.TempDir()
	record := filepath.Join(scratch, "argv.txt")
	bin := scriptedZoektGitIndex(t, record, 0)

	indexer := ZoektIndexer{Bin: bin, IndexDir: filepath.Join(scratch, "index"), Branches: []string{"main", "release"}}
	if err := indexer.Index(context.Background(), "/repo/bare.git"); err != nil {
		t.Fatalf("Index: %v", err)
	}
	got, _ := os.ReadFile(record)
	if !strings.Contains(string(got), "-branches main,release") {
		t.Fatalf("expected joined branches in argv, got %q", got)
	}
}

func TestZoektIndexerCommandFailureIsError(t *testing.T) {
	scratch := t.TempDir()
	bin := scriptedZoektGitIndex(t, filepath.Join(scratch, "argv.txt"), 1)

	indexer := ZoektIndexer{Bin: bin, IndexDir: filepath.Join(scratch, "index")}
	if err := indexer.Index(context.Background(), "/repo/bare.git"); err == nil {
		t.Fatalf("expected an error when zoekt-git-index itself fails")
	}
}

func TestZoektIndexerMissingIndexDirIsError(t *testing.T) {
	indexer := ZoektIndexer{Bin: "irrelevant"}
	if err := indexer.Index(context.Background(), "/repo/bare.git"); err == nil {
		t.Fatalf("expected an error when IndexDir is unset")
	}
}
