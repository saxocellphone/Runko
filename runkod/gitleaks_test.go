package runkod

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/saxocellphone/runko/receive"
)

// scriptedGitleaks writes an executable shell script standing in for
// `gitleaks`, so these tests exercise the real exec.Command/report-parsing
// path without needing a real gitleaks install (unavailable in this
// sandbox - see CLAUDE.md). The script must write reportJSON to whatever
// path follows --report-path in its own argv.
func scriptedGitleaks(t *testing.T, reportJSON string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-gitleaks")
	script := "#!/bin/sh\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--report-path\" ]; then\n" +
		"    cat > \"$arg\" <<'EOF'\n" + reportJSON + "\nEOF\n" +
		"  fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gitleaks script: %v", err)
	}
	return path
}

func TestGitleaksScannerParsesFindings(t *testing.T) {
	// The scanner scans a scratch dir it creates itself (os.MkdirTemp,
	// inside Scan), so the fake script can't know that path in advance to
	// echo a matching "File" field - this test only checks the fields that
	// don't depend on it (RuleID, StartLine); path-stripping behavior is
	// exercised indirectly by every other test's real content round-trip.
	report := `[{"Description":"AWS Access Key","StartLine":3,"RuleID":"aws-access-key","File":"config.py"}]`
	bin := scriptedGitleaks(t, report)
	scanner := GitleaksScanner{Bin: bin}

	findings, err := scanner.Scan([]receive.FileContent{{Path: "config.py", Content: []byte("API_KEY = 'x'\n")}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %+v", findings)
	}
	if findings[0].RuleID != "aws-access-key" || findings[0].Line != 3 {
		t.Fatalf("expected rule/line to pass through, got %+v", findings[0])
	}
}

func TestGitleaksScannerCleanReportIsNoFindings(t *testing.T) {
	bin := scriptedGitleaks(t, `[]`)
	scanner := GitleaksScanner{Bin: bin}

	findings, err := scanner.Scan([]receive.FileContent{{Path: "readme.md", Content: []byte("hello\n")}})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings for a clean report, got %+v", findings)
	}
}

func TestGitleaksScannerNoFilesSkipsInvocation(t *testing.T) {
	// Point at a binary that doesn't exist - if Scan tried to invoke it,
	// this would fail with "executable file not found".
	scanner := GitleaksScanner{Bin: filepath.Join(t.TempDir(), "does-not-exist")}
	findings, err := scanner.Scan(nil)
	if err != nil {
		t.Fatalf("expected no invocation (and no error) with zero files, got %v", err)
	}
	if findings != nil {
		t.Fatalf("expected no findings, got %+v", findings)
	}
}

func TestGitleaksScannerCommandFailureIsError(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-gitleaks")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho boom >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	scanner := GitleaksScanner{Bin: bin}
	_, err := scanner.Scan([]receive.FileContent{{Path: "f.txt", Content: []byte("x")}})
	if err == nil {
		t.Fatalf("expected an error when gitleaks itself fails")
	}
}

// TestGitleaksScannerAgainstRealBinary exercises the real gitleaks CLI if
// it happens to be installed - skipped, not failed, otherwise (unavailable
// in this sandbox; matches the pattern used for git version detection and
// the Bazel integration test).
func TestGitleaksScannerAgainstRealBinary(t *testing.T) {
	if _, err := exec.LookPath("gitleaks"); err != nil {
		t.Skip("gitleaks not installed - skipping real-binary test")
	}
	scanner := GitleaksScanner{}
	// The key is assembled at runtime so this test file's own committed
	// content never pattern-matches a scanner (the self-host import push
	// scans the whole tip tree - docs/migration-findings.md #22); the
	// scanned FileContent still carries the full realistic pattern.
	findings, err := scanner.Scan([]receive.FileContent{
		{Path: "config.py", Content: []byte("aws_secret_access_key = \"AKIA" + "ABCDEFGHIJKLMNOP\"\n")},
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) == 0 {
		t.Fatalf("expected the real gitleaks to flag an AWS-looking key")
	}
}
