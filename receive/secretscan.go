package receive

// SecretScanner scans changed file content for secrets before it becomes
// durable (§11.4, §12.2: "scan at receive, before durability" - purging a
// secret from a snapshot ref afterward is a runbook, not a button).
//
// Design.md is explicit that this must integrate a real scanner
// (gitleaks/trufflehog) rather than bespoke detection heuristics (§11.4:
// "integrate gitleaks/trufflehog; do not build bespoke heuristics"). This
// package therefore only defines the seam - no regex-based secret detector
// lives here, deliberately, and NoOpScanner below is a wiring stub, not a
// stand-in scanner.
type SecretScanner interface {
	Scan(files []FileContent) ([]SecretFinding, error)
}

// FileContent is one changed file's full content, as the scanner needs it.
type FileContent struct {
	Path    string
	Content []byte
}

// SecretFinding is one potential secret detected in a push.
type SecretFinding struct {
	Path        string
	Line        int
	RuleID      string
	Description string
}

// NoOpScanner always reports no findings. It exists so the funnel is
// wireable and testable before a real gitleaks-backed implementation is
// plugged in - it MUST NOT be used in production; there is no secret
// scanning without a real SecretScanner behind this interface.
type NoOpScanner struct{}

func (NoOpScanner) Scan(_ []FileContent) ([]SecretFinding, error) { return nil, nil }
