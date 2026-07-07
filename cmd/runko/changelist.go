// Change lifecycle verbs beyond push/land/approve (§28.3 stage 12c-③):
// list, abandon, rerun-check - thin wrappers over runkod's REST API using
// the same apiJSON plumbing the workspace commands ride.
package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/saxocellphone/runko/checks"
)

// ChangeInfo mirrors runkod.Change's wire shape (Go field names, like
// WorkspaceInfo does for workspaces).
type ChangeInfo struct {
	ChangeKey  string
	State      string
	BaseSHA    string
	HeadSHA    string
	GitRef     string
	Title      string
	LandedSHA  string
	AuthoredBy string
	LandedBy   string
}

// ListChanges fetches GET /api/changes?state= ("" = all states).
func ListChanges(ctx context.Context, client *http.Client, runkodURL, token, state string) ([]ChangeInfo, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/changes"
	if state != "" {
		endpoint += "?state=" + url.QueryEscape(state)
	}
	var list []ChangeInfo
	if err := apiJSON(ctx, client, http.MethodGet, endpoint, token, nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// AbandonChange posts POST /api/changes/{id}/abandon.
func AbandonChange(ctx context.Context, client *http.Client, runkodURL, token, changeID string) (ChangeInfo, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/changes/" + url.PathEscape(changeID) + "/abandon"
	var change ChangeInfo
	if err := apiJSON(ctx, client, http.MethodPost, endpoint, token, nil, &change); err != nil {
		return ChangeInfo{}, err
	}
	return change, nil
}

// RerunCheck posts POST /api/changes/{id}/checks/{name}/rerun and decodes
// the refreshed merge requirements (the same shape approve returns).
func RerunCheck(ctx context.Context, client *http.Client, runkodURL, token, changeID, checkName string) (checks.MergeRequirements, error) {
	endpoint := strings.TrimSuffix(runkodURL, "/") + "/api/changes/" + url.PathEscape(changeID) +
		"/checks/" + url.PathEscape(checkName) + "/rerun"
	var reqs checks.MergeRequirements
	if err := apiJSON(ctx, client, http.MethodPost, endpoint, token, nil, &reqs); err != nil {
		return checks.MergeRequirements{}, err
	}
	return reqs, nil
}
