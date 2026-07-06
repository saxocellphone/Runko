package receive

import "fmt"

// RejectDirectPush renders the §6.9 pre-receive rejection message for a
// direct push to trunk: "a script, not a lecture" - the exact next command,
// a one-line why, and a docs URL. This is the single most alienating moment
// for an engineer with `git push origin main` muscle memory (§6.9), so the
// message must tell them exactly what to type next, not just what failed.
func RejectDirectPush(trunkRef string, docsURL string) string {
	return fmt.Sprintf(
		"remote: Direct pushes to %q are disabled - trunk is closed to direct push (%s).\n"+
			"remote:\n"+
			"remote: Run this instead:\n"+
			"remote:   git push origin HEAD:refs/for/%s\n"+
			"remote: or, with the CLI:\n"+
			"remote:   runko change push\n",
		trunkRef, docsURL, trunkRef,
	)
}
