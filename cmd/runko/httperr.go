package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/saxocellphone/runko/internal/clierr"
)

// decodeAPIError turns a non-2xx runkod REST response into the structured
// clierr.Error the daemon sent (§6.5), falling back to a plain HTTP-status
// error when the body isn't one. Every non-OK status the daemon emits with
// a JSON body - 400, 403, 409 - carries the same shape (runkod/actions.go's
// apiError), so decoding must not be status-specific: pre-fix, `change
// approve` decoded only 400 and a self_approval_denied 403 with a perfectly
// good explanation printed as bare "returned 403".
func decodeAPIError(resp *http.Response, op string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var ce clierr.Error
	if err := json.Unmarshal(body, &ce); err == nil && ce.Message != "" {
		return &ce
	}
	return fmt.Errorf("%s: HTTP %d", op, resp.StatusCode)
}
