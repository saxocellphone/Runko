package gitstore

import (
	"os"
	"strings"

	"github.com/saxocellphone/runko/platform/core"
)

// ErrTagExists reports that CreateAnnotatedTag found the tag name taken -
// callers distinguish it from I/O failures (the release flow surfaces it
// as a structured tag_exists, §14.10.3).
type tagExistsError struct{ name string }

func (e *tagExistsError) Error() string { return "gitstore: tag " + e.name + " already exists" }

// IsTagExists reports whether err is CreateAnnotatedTag's already-exists
// refusal.
func IsTagExists(err error) bool {
	_, ok := err.(*tagExistsError)
	return ok
}

// CreateAnnotatedTag writes an annotated tag object at rev (§14.10.3,
// stage 17b) and returns the TAG object's SHA. The tagger is the server
// identity the land engine already stamps as committer (platform/land):
// releases are minted by the platform on a principal's behalf, and the
// row's created_by carries the human attribution. Refuses to move an
// existing tag - releases are immutable; a wrong one is followed by a
// corrected one, never re-pointed.
func (s *Store) CreateAnnotatedTag(name string, rev core.Revision, message string) (core.Revision, error) {
	if _, err := s.run(nil, "rev-parse", "--verify", "--quiet", "refs/tags/"+name); err == nil {
		return "", &tagExistsError{name: name}
	}
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=Runko", "GIT_AUTHOR_EMAIL=runko@localhost",
		"GIT_COMMITTER_NAME=Runko", "GIT_COMMITTER_EMAIL=runko@localhost",
	)
	if s.ExtraEnv != nil {
		env = append(env, s.ExtraEnv...)
	}
	if _, err := s.run(env, "tag", "-a", name, string(rev), "-m", message); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return "", &tagExistsError{name: name}
		}
		return "", err
	}
	// rev-parse on the tag ref yields the TAG object (annotated tags are
	// their own objects); ^{} would peel to the commit, which callers
	// already know as rev.
	return s.ResolveRef("refs/tags/" + name)
}
