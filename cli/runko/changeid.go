// Change-Id argument resolution: --change accepts a unique prefix of a
// full Change-Id (I + 40 hex), resolved client-side against the change
// list so agents can paste short ids without hitting a misleading
// unknown_change on an otherwise real (often landed) change.
package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// isFullChangeID reports whether s is a complete Change-Id: 'I' plus
// exactly 40 lowercase ASCII hex digits (Gerrit-style, as minted by
// receive.GenerateChangeID). Callers should normalize case first via
// normalizeChangeIDArg.
func isFullChangeID(s string) bool {
	if len(s) != 41 || s[0] != 'I' {
		return false
	}
	for i := 1; i < 41; i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// normalizeChangeIDArg lowercases the hex tail so prefix matching is
// case-insensitive for the hex portion; a leading I/i is forced to 'I'.
// The resolved full id is always lowercase-canonical.
func normalizeChangeIDArg(s string) string {
	if s == "" {
		return s
	}
	if s[0] == 'I' || s[0] == 'i' {
		return "I" + strings.ToLower(s[1:])
	}
	return strings.ToLower(s)
}

// changeUnknownError is the humanized not-found shape used by
// requirements and the comment verbs - names the change, never a raw URL.
func changeUnknownError(changeID string) error {
	return &clierr.Error{
		Code:       "unknown_change",
		Field:      "change",
		Message:    fmt.Sprintf("the control plane has no change %s", changeID),
		Suggestion: "did you `runko change push` yet?",
	}
}

// rewriteChangeNotFound turns apiJSON's bare "URL: not found" into the
// humanized changeUnknownError. Other errors pass through unchanged.
func rewriteChangeNotFound(err error, changeID string) error {
	if err == nil {
		return nil
	}
	var ce *clierr.Error
	if asClierr(err, &ce) && ce.Code == "not_found" {
		return changeUnknownError(changeID)
	}
	return err
}

// rewriteResolveNotFound humanizes the resolve endpoint's 404. That path
// 404s both when the change is missing and when the comment id is wrong
// (server: plain "comment not found"), so the message names both
// possibilities instead of claiming "no change" for a typo'd --comment.
func rewriteResolveNotFound(err error, changeID, commentID string) error {
	if err == nil {
		return nil
	}
	var ce *clierr.Error
	if asClierr(err, &ce) && ce.Code == "not_found" {
		return &clierr.Error{
			Code:       "not_found",
			Field:      "comment",
			Message:    fmt.Sprintf("no change %s, or no comment %s on it", changeID, commentID),
			Suggestion: fmt.Sprintf("runko change comments --change %s", changeID),
		}
	}
	return err
}

// resolveChangeIDArg expands a --change value that may be a unique
// Change-Id prefix into the full id. A full I+40hex id passes through
// with no API call. arg must be non-empty - callers that default to HEAD
// resolve that first and never hand an empty string here.
//
// On a unique prefix match, a one-line note is printed to warnWriter:
// resolved <prefix> -> <full-id> ("<title>").
func resolveChangeIDArg(ctx context.Context, client *http.Client, cred Credential, arg string) (string, error) {
	if arg == "" {
		// Defensive: empty means "guess", and no verb should guess via this path.
		return "", &clierr.Error{
			Code:       "required_field",
			Field:      "change",
			Message:    "change id is required",
			Suggestion: "pass --change <Id> (a full Change-Id or unique prefix)",
		}
	}
	arg = normalizeChangeIDArg(arg)
	if isFullChangeID(arg) {
		return arg, nil
	}

	// All states: a landed change is still a real target for requirements
	// / comments, and a prefix that only hits open would mis-report it as
	// missing (the dogfood papercut that motivated this helper).
	list, err := ListChanges(ctx, client, cred.URL, cred.AuthHeader(), "")
	if err != nil {
		return "", err
	}
	var matches []ChangeInfo
	for _, c := range list {
		if strings.HasPrefix(c.ChangeKey, arg) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return "", changeUnknownError(arg)
	case 1:
		full := matches[0].ChangeKey
		title := matches[0].Title
		if title == "" {
			title = "-"
		}
		fmt.Fprintf(warnWriter, "resolved %s -> %s (%q)\n", arg, full, title)
		return full, nil
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d changes:", arg, len(matches))
		// Cap the listing so a pathological short prefix doesn't dump hundreds
		// of lines; the suggestion is the real next step either way.
		const maxList = 10
		for i, c := range matches {
			if i >= maxList {
				fmt.Fprintf(&b, "\n  ... and %d more", len(matches)-maxList)
				break
			}
			title := c.Title
			if title == "" {
				title = "-"
			}
			fmt.Fprintf(&b, "\n  %s  %s", c.ChangeKey, title)
		}
		return "", &clierr.Error{
			Code:       "ambiguous_change",
			Field:      "change",
			Message:    b.String(),
			Suggestion: "pass more characters of the Change-Id",
		}
	}
}
