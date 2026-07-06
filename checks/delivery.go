package checks

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"
)

// DeliveryAttempt is the result of one HTTP POST attempt to a webhook endpoint.
type DeliveryAttempt struct {
	Success    bool
	StatusCode int
	Err        error
}

// SignatureHeader is the header name webhook receivers must check.
const SignatureHeader = "X-Runko-Signature-256"

// Deliver POSTs a signed webhook payload to url (§14.4.1). It performs
// exactly one attempt - it does not retry internally; see NextBackoff and
// MaxDeliveryAttempts for the retry/DLQ policy a caller (the outbox worker)
// applies between attempts.
func Deliver(ctx context.Context, client *http.Client, url string, payload []byte, secret []byte) DeliveryAttempt {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return DeliveryAttempt{Err: fmt.Errorf("webhook: build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, "sha256="+SignPayload(secret, payload))

	resp, err := client.Do(req)
	if err != nil {
		return DeliveryAttempt{Err: fmt.Errorf("webhook: %w", err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DeliveryAttempt{StatusCode: resp.StatusCode, Err: fmt.Errorf("webhook: endpoint returned %d", resp.StatusCode)}
	}
	return DeliveryAttempt{Success: true, StatusCode: resp.StatusCode}
}

// MaxDeliveryAttempts before a delivery is dead-lettered (§14.4.1).
const MaxDeliveryAttempts = 8

// NextBackoff computes the exponential backoff delay before retry attempt
// n (1-indexed: n=1 is the delay before the second overall attempt), doubling
// from base and capped at maxDelay (§14.4.1's "retries with exponential
// backoff").
func NextBackoff(n int, base, maxDelay time.Duration) time.Duration {
	if n < 1 {
		n = 1
	}
	d := base
	for i := 1; i < n; i++ {
		d *= 2
		if d >= maxDelay {
			return maxDelay
		}
	}
	return d
}
