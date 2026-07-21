// Invite feed over Connect (§13.3.1 first application; §15.1): the
// mailer's drain surface is served from runkod's own in-boundary contract
// (runkod/proto/mailer/v1), superseding the REST drain endpoints. Same
// operator gate, same store calls, same backoff constants (invite.go) -
// only the transport changed, and the consumer now rides a declared
// dependency edge instead of a copy-pasted struct.
package runkod

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/saxocellphone/runko/internal/clierr"
	mailerv1 "github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1"
	"github.com/saxocellphone/runko/runkod/proto/gen/mailer/v1/mailerv1connect"
)

var _ mailerv1connect.InviteFeedServiceHandler = (*rpcServer)(nil)

// requireOperatorRPC is requireOperator's Connect-route sibling: the feed
// rows carry PII (emails) and the acks are writes, so bot lanes, agents,
// and stored accounts are all refused - only the deploy token and
// operator principals pass. Bare 403 -> CodePermissionDenied on Connect
// clients (the rpcMiddleware convention for auth-shaped refusals).
func (s *Server) requireOperatorRPC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := s.callerForAuthHeader(r.Header.Get("Authorization"))
		if c.lane != nil || !isOperator(c) {
			http.Error(w, "forbidden: invite requests are an operator surface", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (r *rpcServer) ListDue(ctx context.Context, _ *connect.Request[mailerv1.ListDueRequest]) (*connect.Response[mailerv1.ListDueResponse], error) {
	reqs, err := r.s.Store.ListDueInviteRequests(ctx, time.Now())
	if err != nil {
		return nil, connectErr(internalErr(err))
	}
	out := make([]*mailerv1.InviteRequest, len(reqs))
	for i, q := range reqs {
		out[i] = &mailerv1.InviteRequest{
			Id: q.ID, Kind: q.Kind, Name: q.Name, Email: q.Email, Message: q.Message,
			Attempt: int32(q.Attempt), CreatedAt: timestamppb.New(q.CreatedAt),
		}
	}
	return connect.NewResponse(&mailerv1.ListDueResponse{Requests: out}), nil
}

func (r *rpcServer) MarkSent(ctx context.Context, req *connect.Request[mailerv1.MarkSentRequest]) (*connect.Response[mailerv1.AckResponse], error) {
	return r.ackInviteRequest(ctx, req.Msg.Id, "")
}

func (r *rpcServer) MarkFailed(ctx context.Context, req *connect.Request[mailerv1.MarkFailedRequest]) (*connect.Response[mailerv1.AckResponse], error) {
	sendErr := req.Msg.Error
	if sendErr == "" {
		// A failure ack without a reason still fails the row: "" means
		// success to the store, so it must never pass through.
		sendErr = "unspecified mailer failure"
	}
	return r.ackInviteRequest(ctx, req.Msg.Id, sendErr)
}

func (r *rpcServer) ackInviteRequest(ctx context.Context, id, sendErr string) (*connect.Response[mailerv1.AckResponse], error) {
	req, err := r.s.Store.RecordInviteSendResult(ctx, id, sendErr,
		inviteBackoffBase, inviteBackoffMax, time.Now())
	if errors.Is(err, errNoInviteRequest) {
		return nil, connectErr(typedErr(http.StatusNotFound, clierr.Error{
			Code: "unknown_invite_request", Field: "id",
			Message:    fmt.Sprintf("no invite request %q", id),
			Suggestion: "re-poll InviteFeedService.ListDue",
		}))
	}
	if err != nil {
		return nil, connectErr(internalErr(err))
	}
	return connect.NewResponse(&mailerv1.AckResponse{
		Id: req.ID, Status: req.Status, Attempt: int32(req.Attempt),
		NextAttemptAt: timestamppb.New(req.NextAttemptAt),
	}), nil
}
