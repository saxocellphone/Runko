package receive

import "strings"

const magicRefPrefix = "refs/for/"

// ParseMagicRef extracts the trunk branch name from a magic-ref push target
// (§11.5), e.g. "refs/for/main" -> ("main", true).
func ParseMagicRef(ref string) (trunk string, ok bool) {
	if !strings.HasPrefix(ref, magicRefPrefix) {
		return "", false
	}
	trunk = strings.TrimPrefix(ref, magicRefPrefix)
	if trunk == "" {
		return "", false
	}
	return trunk, true
}

// IsDirectTrunkPush reports whether ref is a direct push to the trunk branch
// (refs/heads/<trunkRef>) rather than the magic ref - the case §6.9 designs
// the rejection UX for.
func IsDirectTrunkPush(ref string, trunkRef string) bool {
	return ref == "refs/heads/"+trunkRef
}
