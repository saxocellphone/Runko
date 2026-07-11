package gitstore

import (
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// TestCreateAnnotatedTag pins the stage-17b tag primitive: a real
// annotated tag object at the given rev, tagged by the server identity,
// refusing to move an existing tag (IsTagExists distinguishes the
// refusal).
func TestCreateAnnotatedTag(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a.txt", "hi\n")
	head := repo.Commit("initial")

	store := New(repo.Dir)
	tagSHA, err := store.CreateAnnotatedTag("commerce/checkout/v1.0.0", "HEAD", "Release checkout 1.0.0\n\n- first")
	if err != nil {
		t.Fatalf("CreateAnnotatedTag: %v", err)
	}
	if string(tagSHA) == head {
		t.Fatalf("expected the TAG object SHA, got the commit itself")
	}
	out, err := store.run(nil, "cat-file", "-p", string(tagSHA))
	if err != nil {
		t.Fatalf("cat-file: %v", err)
	}
	if !strings.Contains(out, "tagger Runko <runko@localhost>") || !strings.Contains(out, "Release checkout 1.0.0") {
		t.Fatalf("unexpected tag object:\n%s", out)
	}
	peeled, err := store.ResolveRef("refs/tags/commerce/checkout/v1.0.0^{commit}")
	if err != nil || string(peeled) != head {
		t.Fatalf("tag must peel to the tagged commit, got %q err=%v", peeled, err)
	}

	_, err = store.CreateAnnotatedTag("commerce/checkout/v1.0.0", "HEAD", "again")
	if !IsTagExists(err) {
		t.Fatalf("expected IsTagExists, got %v", err)
	}
}
