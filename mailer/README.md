# mailer

`runko-mailer` is the invite-request notifier (design.md §15.1): a
stateless, egress-only poller that drains runkod's operator-only
invite-request feed and emails each request to the deployment operator
over plain SMTP, with `Reply-To:` set to the requester — replying with
the invite code is the whole fulfillment loop.

- Deployment shape mirrors `runko-watchdog`: ships in the runkod image,
  runs as its own single-replica Deployment, flags with `RUNKO_MAILER_*`
  env fallbacks, `/healthz` for probes, `--once` for smoke runs.
- Retry, backoff, and dead-letter state live on the server row (the
  §14.4.1 outbox model) — a mailer crash or redeploy never loses a
  request; delivery is at-least-once.
- Gmail: point `--smtp-addr` at `smtp.gmail.com:587` with an app
  password; the From gets rewritten to the authenticated account, which
  is fine — Reply-To carries the requester.

Scaffolded by `runko project create` (type `service`, lang `go`).
