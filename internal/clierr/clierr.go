// Package clierr is the CLI/agent-facing structured error shape mandated by
// docs/design.md §6.5: "{ code, field, message, suggestion, doc_url }" for
// CLI, UI, and agents alike - never a raw tool exit status ("exit status
// 128") surfacing as the only explanation for a failure.
package clierr

import "strings"

// Error is a resolve-or-explain error: Message says what went wrong,
// Suggestion says the next command/action to take. Field names the CLI
// flag or input the error concerns, for machine consumers (agents) that
// want to highlight it without parsing Message.
type Error struct {
	Code       string
	Field      string
	Message    string
	Suggestion string
	DocURL     string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Message)
	if e.Suggestion != "" {
		b.WriteString("\n  -> ")
		b.WriteString(e.Suggestion)
	}
	if e.DocURL != "" {
		b.WriteString("\n  see ")
		b.WriteString(e.DocURL)
	}
	return b.String()
}

// unresolvableRevisionMarkers are substrings of git's own fatal/error text
// when a revision argument doesn't resolve - matched here so callers can
// recognize the *class* of failure without depending on git's exact wording
// staying stable, and without misclassifying unrelated errors (e.g. a
// network failure) as a bad revision.
var unresolvableRevisionMarkers = []string{
	"unknown revision",
	"bad revision",
	"did not match any file(s) known to git", // `git checkout <bad-rev>`
	"not a tree",                             // rev resolves to a non-commit object
}

// WrapRevisionError turns a git error over a user-supplied revision into a
// structured, resolve-or-explain Error (§6.5) instead of git's raw
// "ambiguous argument ... unknown revision" text. field/value name the CLI
// flag and value that failed to resolve. If err doesn't look like an
// unresolvable-revision failure, it is returned unchanged - this only
// reclassifies the specific failure mode it recognizes.
func WrapRevisionError(err error, field, value string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	matched := false
	for _, marker := range unresolvableRevisionMarkers {
		if strings.Contains(msg, marker) {
			matched = true
			break
		}
	}
	if !matched {
		return err
	}
	return &Error{
		Code:       "unknown_revision",
		Field:      field,
		Message:    field + " " + quote(value) + " does not resolve to a commit in this repository",
		Suggestion: "check the spelling, and that the ref/SHA has been fetched (`git fetch` may be needed)",
		DocURL:     "docs/design.md#65-validation-ux-fix-boq-style-late-failure",
	}
}

func quote(s string) string {
	return "\"" + s + "\""
}

// WrapRevisionErrorAmong is WrapRevisionError for call sites with more than
// one user-supplied revision in play (e.g. `--base`/`--head`), where git's
// error text names exactly which one failed to resolve (e.g. "ambiguous
// argument 'badbase'"). candidates maps CLI flag name to the value supplied
// for it; the field whose value is quoted in err's message is used. If no
// candidate value appears in the message, err is returned unchanged rather
// than guessing.
func WrapRevisionErrorAmong(err error, candidates map[string]string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for field, value := range candidates {
		if value != "" && strings.Contains(msg, value) {
			return WrapRevisionError(err, field, value)
		}
	}
	return err
}
