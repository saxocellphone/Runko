#!/usr/bin/env bash
# Edge-case suite over the real compose stack - the invariants that only
# mean something through the full transport (real git push -> smart-HTTP ->
# CGI -> hook -> daemon -> Postgres). See docs/smoke-plan.md for the
# selection rationale and what deliberately lives in the Go suites instead.
#
# Runs after scripts/compose-smoke.sh in CI (that script is the frozen,
# timed §16.4 claim; this one is where assertions accumulate). Own stack
# lifecycle, fresh volumes - the smoke's image build keeps this one cheap.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)

BASE_URL=${BASE_URL:-http://localhost:8080}
export RUNKO_GLOBAL_REQUIRED_CHECKS=smoke-check
export RUNKO_PRINCIPALS='name=alice;token=alice-dev-token|name=bob;token=bob-dev-token|name=bumpbot;token=bot-dev-token;agent'
ALICE=alice-dev-token
BOB=bob-dev-token
BOT=bot-dev-token

step() { echo; echo "=== $1"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

cleanup() {
  cd "$ROOT" || return
  docker compose logs runkod --tail 60 || true
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

# expect_fail <desc> <cmd...>: the command MUST fail; its combined output
# lands in $CAPTURED for expect_in.
CAPTURED=""
expect_fail() {
  local desc=$1; shift
  set +e
  CAPTURED=$("$@" 2>&1)
  local rc=$?
  set -e
  if [ $rc -eq 0 ]; then
    echo "$CAPTURED"
    fail "$desc: expected failure, command succeeded"
  fi
}
expect_in() { # <needle> <desc>
  echo "$CAPTURED" | grep -qi "$1" || { echo "$CAPTURED"; fail "$2: output does not mention '$1'"; }
}

# api <method> <path> <token>: sets $HTTP_CODE, leaves the body in a file
# read via body(). NOT command-substitution-safe by design - $(api ...)
# would set HTTP_CODE in a subshell and lose it.
HTTP_CODE=""
API_BODY=$(mktemp)
api() {
  local method=$1 path=$2 token=$3; shift 3
  HTTP_CODE=$(curl -s -o "$API_BODY" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $token" "$@" "$BASE_URL$path")
}
body() { cat "$API_BODY"; }

json_get() { # <json-on-stdin> via args: <python expr over j>
  python3 -c "import json,sys; j=json.load(sys.stdin); print($1)"
}

step "build CLIs + fresh stack"
BIN=$(mktemp -d)
go build -o "$BIN/runko" ./cli/runko
go build -o "$BIN/runko-ci" ./cli/runko-ci
docker compose up -d --build
for i in $(seq 1 120); do
  curl -fsS "$BASE_URL/readyz" >/dev/null 2>&1 && break
  [ "$i" = 120 ] && fail "daemon never became ready"
  sleep 2
done

ALICE_URL=$(echo "$BASE_URL" | sed "s|http://|http://alice:$ALICE@|")/monorepo.git/
BOB_URL=$(echo "$BASE_URL" | sed "s|http://|http://bob:$BOB@|")/monorepo.git/
BOT_URL=$(echo "$BASE_URL" | sed "s|http://|http://bumpbot:$BOT@|")/monorepo.git/

WORK=$(mktemp -d)
git clone "$ALICE_URL" "$WORK/mono" 2>&1 | grep -v '^warning:' || true
cd "$WORK/mono"
git config user.name alice
git config user.email alice@example.com

# gate_and_land <change-id>: green-light the global check, approve as bob
# (alice authored - self-approval is E6's concern, not repeated here), land.
gate_and_land() {
  "$BIN/runko-ci" report-check --url "$BASE_URL/api/changes/$1/checks" \
    --token "$ALICE" --name smoke-check --external-id "job-$RANDOM" \
    --status completed --conclusion success --reporter edgecases >/dev/null
  "$BIN/runko" change approve --change "$1" --owner group:commerce-eng \
    --by bob --runkod-url "$BASE_URL" --token "$BOB" >/dev/null
  "$BIN/runko" change land --change "$1" --runkod-url "$BASE_URL" --token "$BOB" >/dev/null
}
push_change() { # prints the Change-Id
  "$BIN/runko" change push --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["change_id"])'
}

step "seed: create project, land it"
"$BIN/runko" project create --name checkout-api --type service --api none --owners group:commerce-eng >/dev/null
gate_and_land "$(push_change)"
git fetch -q origin main && git reset -q --hard FETCH_HEAD
PROJECT_DIR=$(dirname "$(git ls-files | grep PROJECT.yaml | head -1)")

step "E1: authn - bad tokens refused on both surfaces, /healthz open"
expect_fail "git with wrong token" git ls-remote "$(echo "$BASE_URL" | sed 's|http://|http://x:wrong@|')/monorepo.git/"
api GET /api/changes wrong-token
[ "$HTTP_CODE" = "401" ] || fail "E1: REST with wrong token returned $HTTP_CODE, want 401"
curl -fsS "$BASE_URL/healthz" >/dev/null || fail "E1: /healthz must not require auth"

step "E2: direct trunk push rejected with the §6.9 script"
# The pushed commit must be one trunk doesn't have: pushing trunk's own
# tip back is "Everything up-to-date" client-side - the hook never runs.
git checkout -q -b trunkpush
printf 'x\n' > "$PROJECT_DIR/direct.txt"
git add -A && git commit -q -m "direct-to-trunk attempt"
expect_fail "direct trunk push" git push origin HEAD:refs/heads/main
expect_in "closed to direct push" "E2"
expect_in "refs/for" "E2 (must name the fix)"
git checkout -q - && git branch -q -D trunkpush

step "E3: non-funnel refs pass unconditionally (§14.10.3 documented permissiveness)"
# This PINS the current contract: only refs/heads/<trunk>, refs/for/* and
# refs/workspaces/* are policed; tags (and any other namespace) pass. If
# tag governance ever tightens (§14.10.3's open question), this assertion
# is the contract change's tripwire.
git tag v0.0.1-smoke
git push -q origin v0.0.1-smoke || fail "E3: tag push must be accepted (documented permissiveness)"

step "E4: real gitleaks rejects a pushed secret before durability"
git checkout -q -b leak
# Both secrets are assembled at runtime so THIS script's own tree never
# pattern-matches a scanner - the pushed config.py still carries the full
# realistic patterns E4 needs. Discovered the hard way: the self-host
# import push (docs/migration-findings.md #22) was rejected by prod's
# gitleaks because the contiguous AKIA literal used to live on this line.
printf 'aws_key = "AKIA%s"\ngithub_pat = "ghp_%s"\n' \
  "Q7RANDOM9KEYXY12" \
  "abcdefghijklmnopqrstuvwxyz0123456789" > "$PROJECT_DIR/config.py"
git add -A && git commit -q -m "add config"
expect_fail "secret push" git push origin +HEAD:refs/for/main
expect_in "possible secret" "E4"
git checkout -q - && git branch -q -D leak

step "E5: agent principal's direct refs/for push refused (§8.7 default policy)"
git checkout -q -b agentpush
printf '// benign\n' >> "$PROJECT_DIR/main.go"
git add -A && git commit -q -m "agent edit"
expect_fail "agent direct push" git push "$BOT_URL" +HEAD:refs/for/main
expect_in "policy violation" "E5"
expect_in "workspace" "E5 (affinity is the named reason)"
git checkout -q - && git branch -q -D agentpush

step "E6: amend resets BOTH gates (§13.5 approval binding, head-keyed checks)"
git checkout -q -b amendcase
printf '\n// v1\n' >> "$PROJECT_DIR/main.go"
git add -A && git commit -q -m "amendable change"
ID=$(push_change)
"$BIN/runko-ci" report-check --url "$BASE_URL/api/changes/$ID/checks" \
  --token "$ALICE" --name smoke-check --external-id job-e6 \
  --status completed --conclusion success --reporter edgecases >/dev/null
"$BIN/runko" change approve --change "$ID" --owner group:commerce-eng \
  --by bob --runkod-url "$BASE_URL" --token "$BOB" >/dev/null
api GET "/api/changes/$ID/merge-requirements" "$ALICE"
[ "$(body | json_get 'j["mergeable"]')" = "True" ] || fail "E6: expected mergeable before amend"
printf '// v2 - never reviewed\n' >> "$PROJECT_DIR/main.go"
git add -A && git commit -q --amend --no-edit
push_change >/dev/null
api GET "/api/changes/$ID/merge-requirements" "$ALICE"
[ "$(body | json_get 'j["mergeable"]')" = "False" ] || fail "E6: amend must reset mergeability"
body | json_get 'len(j["owners"]["outstanding"])' | grep -q '^1$' || fail "E6: owner gate must reset on amend"
body | json_get '"smoke-check" in j["checks"]["pending"]' | grep -q True || fail "E6: check gate must reset on amend"
gate_and_land "$ID"
git checkout -q - && git branch -q -D amendcase
git fetch -q origin main && git reset -q --hard FETCH_HEAD

step "E7: opt-in affected-intersection revalidation (§13.5; the compose daemon pins RUNKO_REVALIDATION=affected-intersection - the conflict-only default lands this with zero re-runs) - 409, rebase, re-gate, land"
git checkout -q -b changeA
printf 'a\n' > "$PROJECT_DIR/fileA.txt"
git add -A && git commit -q -m "change A"
ID_A=$(push_change)
git checkout -q - && git checkout -q -b changeB
printf 'b\n' > "$PROJECT_DIR/fileB.txt"
git add -A && git commit -q -m "change B"
ID_B=$(push_change)
gate_and_land "$ID_A"
# Gate B fully, then land: trunk's delta (A) intersects B's affected set.
"$BIN/runko-ci" report-check --url "$BASE_URL/api/changes/$ID_B/checks" \
  --token "$ALICE" --name smoke-check --external-id job-e7 \
  --status completed --conclusion success --reporter edgecases >/dev/null
"$BIN/runko" change approve --change "$ID_B" --owner group:commerce-eng \
  --by bob --runkod-url "$BASE_URL" --token "$BOB" >/dev/null
api POST "/api/changes/$ID_B/land" "$BOB"
[ "$HTTP_CODE" = "409" ] || fail "E7: expected 409 for intersecting trunk delta, got $HTTP_CODE: $(body)"
body | json_get 'j["Code"]' | grep -q requires_revalidation || fail "E7: expected requires_revalidation, got $(body)"
# The way out §13.5 prescribes: rebase onto trunk, re-push (same
# Change-Id), gates reset by the amend semantics, re-gate, land.
git fetch -q origin main && git rebase -q FETCH_HEAD
push_change >/dev/null
gate_and_land "$ID_B"
git fetch -q origin main && git reset -q --hard FETCH_HEAD
[ -f "$PROJECT_DIR/fileA.txt" ] && [ -f "$PROJECT_DIR/fileB.txt" ] || fail "E7: trunk must contain both changes"
# Re-anchor local main at the trunk tip (we are on changeB, already reset
# there) - a stale local main would make every LATER change base off old
# trunk and trip revalidation where none is being tested.
git checkout -q -B main
git branch -q -D changeA changeB

step "E8: workspace snapshots - owner-only (§12.2), ghost refs refused"
"$BIN/runko" workspace create --name wsdemo --project checkout-api --by alice \
  --runkod-url "$BASE_URL" --token "$ALICE" \
  --clone-dir "$WORK/wsclone" --dir "$WORK/wsdemo" >/dev/null
printf 'wip\n' > "$WORK/wsdemo/$PROJECT_DIR/wip.txt"
# §12.7: the store is credential-neutral (no token in the remote URL), so
# the flagless snapshot verb resolves auth env > stored login. This eval
# runs from a bare environment with neither - the env form IS the
# documented contract for exactly this shape (cli-contract.md).
RUNKO_RUNKOD_URL="$BASE_URL" RUNKO_TOKEN="$ALICE" \
  "$BIN/runko" workspace snapshot --dir "$WORK/wsdemo" >/dev/null
git ls-remote "$ALICE_URL" "refs/workspaces/wsdemo/head" | grep -q . || fail "E8: owner's snapshot must be durable"
expect_fail "bob pushing alice's snapshot ref" git -C "$WORK/mono" push "$BOB_URL" +HEAD:refs/workspaces/wsdemo/head
expect_in "belongs to alice" "E8"
expect_fail "ghost workspace ref" git -C "$WORK/mono" push "$BOB_URL" +HEAD:refs/workspaces/ghost/head
expect_in "is registered" "E8 (unregistered id)"

step "E9: restart durability - Postgres+volumes survive, migrator no-ops, daemon still lands"
LANDED_BEFORE=$("$BIN/runko" change list --state landed --runkod-url "$BASE_URL" --token "$ALICE" --json | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
docker compose -f "$ROOT/docker-compose.yml" restart runkod >/dev/null 2>&1
for i in $(seq 1 60); do
  curl -fsS "$BASE_URL/readyz" >/dev/null 2>&1 && break
  [ "$i" = 60 ] && fail "E9: daemon never came back after restart"
  sleep 2
done
LANDED_AFTER=$("$BIN/runko" change list --state landed --runkod-url "$BASE_URL" --token "$ALICE" --json | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
[ "$LANDED_BEFORE" = "$LANDED_AFTER" ] || fail "E9: landed count changed across restart ($LANDED_BEFORE -> $LANDED_AFTER)"
MIGRATIONS=$(docker compose -f "$ROOT/docker-compose.yml" logs runkod | grep -c "applied schema migrations" || true)
[ "$MIGRATIONS" = "1" ] || fail "E9: migrator must be a no-op on restart (saw $MIGRATIONS applications)"
git checkout -q -b postrestart
printf 'post-restart\n' > "$PROJECT_DIR/restart.txt"
git add -A && git commit -q -m "post-restart change"
gate_and_land "$(push_change)"
git checkout -q - && git branch -q -D postrestart

step "E10: /metrics gauges truthful at the known end state"
METRICS=$(curl -fsS "$BASE_URL/metrics")
echo "$METRICS" | grep -q "runkod_up 1" || fail "E10: runkod_up"
echo "$METRICS" | grep -q "runkod_open_changes 0" || { echo "$METRICS"; fail "E10: expected 0 open changes at end state"; }

step "E11: MCP stdio round-trip against the composed daemon"
MCP_OUT=$(printf '%s\n%s\n%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"edgecases","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}' \
  | "$BIN/runko" mcp serve --runkod-url "$BASE_URL" --token "$ALICE")
echo "$MCP_OUT" | python3 -c '
import json, sys
lines = [json.loads(l) for l in sys.stdin if l.strip()]
result = json.loads(lines[1]["result"]["content"][0]["text"])
names = [p["name"] for p in result["projects"]]
assert "checkout-api" in names, names
print("mcp ok:", names)'

echo
echo "==> all edge cases passed"
