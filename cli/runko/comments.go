// Review conversation verbs (§13.4.1-13.4.2, §28.3 stage 16): comment,
// comments, resolve, request-review - thin wrappers over runkod's REST API
// using the same apiJSON plumbing the workspace and change-lifecycle verbs
// ride. Agents comment through exactly this path (CLI-first, §8.3); the
// server refuses their approvals, never their comments.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// ActorInfo mirrors the wire's Actor shape (§7.5).
type ActorInfo struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// CommentInfo mirrors runkod's ChangeComment wire shape
// (docs/spec/mcp-tools/common.schema.json#/$defs/ChangeComment).
type CommentInfo struct {
	ID        string    `json:"id"`
	Author    ActorInfo `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Path      string    `json:"path,omitempty"`
	Side      string    `json:"side,omitempty"`
	Line      int       `json:"line,omitempty"`
	HeadSHA   string    `json:"head_sha,omitempty"`
	ParentID  string    `json:"parent_id,omitempty"`
	Resolved  bool      `json:"resolved,omitempty"`
}

type commentsPage struct {
	Comments      []CommentInfo `json:"comments"`
	NextPageToken string        `json:"next_page_token,omitempty"`
}

// CreateComment posts POST /api/changes/{id}/comments (§13.4.1). The server
// stamps head_sha and validates the anchor and the one-level thread rule.
func CreateComment(ctx context.Context, client *http.Client, runkodURL, token, changeID string, body map[string]any) (CommentInfo, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/changes/" + url.PathEscape(changeID) + "/comments"
	var comment CommentInfo
	if err := apiJSON(ctx, client, http.MethodPost, endpoint, token, body, &comment); err != nil {
		return CommentInfo{}, err
	}
	return comment, nil
}

// ListComments fetches GET /api/changes/{id}/comments.
func ListComments(ctx context.Context, client *http.Client, runkodURL, token, changeID string) (commentsPage, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/changes/" + url.PathEscape(changeID) + "/comments"
	var page commentsPage
	if err := apiJSON(ctx, client, http.MethodGet, endpoint, token, nil, &page); err != nil {
		return commentsPage{}, err
	}
	return page, nil
}

// ResolveComment posts POST /api/changes/{id}/comments/{cid}/resolve.
func ResolveComment(ctx context.Context, client *http.Client, runkodURL, token, changeID, commentID string, resolved bool) (CommentInfo, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/changes/" + url.PathEscape(changeID) +
		"/comments/" + url.PathEscape(commentID) + "/resolve"
	var comment CommentInfo
	if err := apiJSON(ctx, client, http.MethodPost, endpoint, token, map[string]any{"resolved": resolved}, &comment); err != nil {
		return CommentInfo{}, err
	}
	return comment, nil
}

// RequestReview posts POST /api/changes/{id}/request-review (§13.4.2).
func RequestReview(ctx context.Context, client *http.Client, runkodURL, token, changeID, reviewer string) error {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/changes/" + url.PathEscape(changeID) + "/request-review"
	var out map[string]any
	return apiJSON(ctx, client, http.MethodPost, endpoint, token, map[string]any{"reviewer": reviewer}, &out)
}

// GetChangeInfo fetches GET /api/changes/{id} - used by `change comments` to
// mark comments whose head_sha is no longer the current head as outdated.
func GetChangeInfo(ctx context.Context, client *http.Client, runkodURL, token, changeID string) (ChangeInfo, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/changes/" + url.PathEscape(changeID)
	var change ChangeInfo
	if err := apiJSON(ctx, client, http.MethodGet, endpoint, token, nil, &change); err != nil {
		return ChangeInfo{}, err
	}
	return change, nil
}

// resolveChangeFlag returns --change or falls back to HEAD's Change-Id
// trailer, the `change requirements` convention. The HEAD lookup honors
// -w/--workspace (resolveWorkspaceDir) so the fallback works from
// anywhere, not just inside the worktree.
func resolveChangeFlag(changeID, workspace, dir string) (string, error) {
	if changeID != "" {
		return changeID, nil
	}
	wd, err := resolveWorkspaceDir(workspace, dir)
	if err != nil {
		return "", err
	}
	return headChangeID(wd)
}

func newChangeCommentCmd(a *app) *cobra.Command {
	var (
		changeID, dir, msg, file, side, replyTo string
		line                                    int
		jsonOut                                 bool
	)
	cmd := &cobra.Command{
		Use:   "comment -m <text>",
		Short: "Leave an anchored review comment",
		Long: `Comments on a change (§13.4.1): change-level (no --file), file-level
(--file), or line-level (--file --line; --side defaults to head). The
server stamps the head SHA, so an amend marks the comment outdated.
--reply-to replies in a thread (one level deep). Agent principals
comment with the agent badge; their approvals stay refused.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if msg == "" {
				return fmt.Errorf("change comment: -m is required")
			}
			id, err := resolveChangeFlag(changeID, mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			body := map[string]any{"body": msg}
			if file != "" {
				body["path"] = file
			}
			if line != 0 {
				body["line"] = line
			}
			if side != "" {
				body["side"] = side
			}
			if replyTo != "" {
				body["parent_id"] = replyTo
			}
			comment, err := CreateComment(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, body)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(comment)
			}
			fmt.Printf("commented on %s (%s)\n", id, comment.ID)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id (default: HEAD's Change-Id trailer)")
	fl.StringVar(&dir, "dir", ".", "repository directory (for the HEAD default)")
	addWorkspaceFlag(cmd)
	fl.StringVarP(&msg, "message", "m", "", "comment body (required)")
	fl.StringVar(&file, "file", "", "file-level or line-level anchor path")
	fl.IntVar(&line, "line", 0, "line anchor (needs --file)")
	fl.StringVar(&side, "side", "", "line anchor side: head (default) or base")
	fl.StringVar(&replyTo, "reply-to", "", "thread root comment id to reply to (replies inherit its anchor)")
	fl.BoolVar(&jsonOut, "json", false, "emit the created comment as JSON")
	return cmd
}

func newChangeCommentsCmd(a *app) *cobra.Command {
	var (
		changeID, dir string
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "comments",
		Short: "List review threads on a Change",
		Long: `Lists the change's review threads (§13.4.1), grouped with replies
indented, marking [resolved]/[outdated]/[agent].`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := resolveChangeFlag(changeID, mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			ctx := context.Background()
			page, err := ListComments(ctx, http.DefaultClient, cred.URL, cred.AuthHeader(), id)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(page)
			}
			if len(page.Comments) == 0 {
				fmt.Printf("%s: no comments\n", id)
				return nil
			}
			// Outdated = written against an older head (§13.4.1 - marked, never
			// repositioned). Degrade to no marking if the change fetch fails.
			currentHead := ""
			if change, err := GetChangeInfo(ctx, http.DefaultClient, cred.URL, cred.AuthHeader(), id); err == nil {
				currentHead = change.HeadSHA
			}
			printCommentThreads(id, page.Comments, currentHead)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id (default: HEAD's Change-Id trailer)")
	fl.StringVar(&dir, "dir", ".", "repository directory (for the HEAD default)")
	addWorkspaceFlag(cmd)
	fl.BoolVar(&jsonOut, "json", false, "emit {comments, next_page_token} as JSON")
	return cmd
}

// printCommentThreads renders roots in creation order with their replies
// indented beneath them - the thread view, flat-file edition.
func printCommentThreads(changeID string, comments []CommentInfo, currentHead string) {
	fmt.Printf("%s: %d comment(s)\n", changeID, len(comments))
	byParent := map[string][]CommentInfo{}
	for _, c := range comments {
		byParent[c.ParentID] = append(byParent[c.ParentID], c)
	}
	for _, root := range byParent[""] {
		fmt.Println(formatComment(root, currentHead, ""))
		for _, reply := range byParent[root.ID] {
			fmt.Println(formatComment(reply, currentHead, "    "))
		}
	}
}

func formatComment(c CommentInfo, currentHead, indent string) string {
	var marks []string
	if c.Author.Type == "agent" {
		marks = append(marks, "agent")
	}
	if currentHead != "" && c.HeadSHA != "" && c.HeadSHA != currentHead {
		marks = append(marks, "outdated")
	}
	if c.ParentID == "" && c.Resolved {
		marks = append(marks, "resolved")
	}
	mark := ""
	if len(marks) > 0 {
		mark = " [" + strings.Join(marks, ", ") + "]"
	}
	anchor := ""
	switch {
	case c.Path != "" && c.Line > 0:
		anchor = fmt.Sprintf(" %s:%d", c.Path, c.Line)
	case c.Path != "":
		anchor = " " + c.Path
	}
	return fmt.Sprintf("%s%s  %s%s%s\n%s    %s", indent, c.ID, c.Author.ID, anchor, mark, indent, c.Body)
}

func newChangeResolveCmd(a *app) *cobra.Command {
	var (
		changeID, dir string
		undo, jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "resolve <comment-id>",
		Short: "Resolve or reopen a review thread",
		Long: `Resolves a thread root (§13.4.1) - allowed for the thread author, the
change author, an owner of the anchored path, or an admin. --undo
reopens.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			commentID, err := requireArg(cmd, args, "comment id")
			if err != nil {
				return err
			}
			id, err := resolveChangeFlag(changeID, mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			comment, err := ResolveComment(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, commentID, !undo)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(comment)
			}
			if comment.Resolved {
				fmt.Printf("resolved thread %s on %s\n", commentID, id)
			} else {
				fmt.Printf("reopened thread %s on %s\n", commentID, id)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id (default: HEAD's Change-Id trailer)")
	fl.StringVar(&dir, "dir", ".", "repository directory (for the HEAD default)")
	addWorkspaceFlag(cmd)
	fl.BoolVar(&undo, "undo", false, "reopen the thread instead of resolving it")
	fl.BoolVar(&jsonOut, "json", false, "emit the updated comment as JSON")
	return cmd
}

func newChangeRequestReviewCmd(a *app) *cobra.Command {
	var (
		changeID, dir string
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "request-review <principal|group:name>",
		Short: "Ask a principal or group to review",
		Long: `Records the review request (idempotent) and puts the reviewer in the
derived attention set (§13.4.2) until they approve or comment at the
current head.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			reviewer, err := requireArg(cmd, args, "reviewer")
			if err != nil {
				return err
			}
			id, err := resolveChangeFlag(changeID, mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			if err := RequestReview(context.Background(), http.DefaultClient, cred.URL, cred.AuthHeader(), id, reviewer); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"reviewer": reviewer})
			}
			fmt.Printf("requested review from %s on %s - they enter the attention set (§13.4.2)\n", reviewer, id)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&changeID, "change", "", "Change-Id (default: HEAD's Change-Id trailer)")
	fl.StringVar(&dir, "dir", ".", "repository directory (for the HEAD default)")
	addWorkspaceFlag(cmd)
	fl.BoolVar(&jsonOut, "json", false, "emit {reviewer} as JSON")
	return cmd
}
