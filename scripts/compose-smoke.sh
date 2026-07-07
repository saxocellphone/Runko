#!/usr/bin/env bash
# The §16.4 measured eval loop (§28.3 stage 14), run in CI on every push so
# §3.3's "< 15 minutes" claim cannot rot:
#
#   compose up -> create project -> open Change -> land
#             -> edit -> open Change -> land
#
# timed end to end (INCLUDING the image build - the claim covers a cold
# evaluator's wall clock, not a warmed cache), and gated by REAL policy:
# the created project declares owners, the daemon requires a global check
# (§14.9), alice pushes and cannot approve her own change (§8.7) - bob
# approves, the check must be green, only then does land succeed.
#
# Requires: docker compose v2, go (to build the runko/runko-ci CLIs), git.
set -euo pipefail
cd "$(dirname "$0")/.."

BUDGET_SECONDS=${BUDGET_SECONDS:-900}
BASE_URL=${BASE_URL:-http://localhost:8080}
export RUNKO_GLOBAL_REQUIRED_CHECKS=smoke-check

ALICE_TOKEN=alice-dev-token
BOB_TOKEN=bob-dev-token

ROOT=$(pwd)
cleanup() {
  # The loop cd's into its throwaway clone; compose commands need the
  # repo root (where docker-compose.yml lives) - found the hard way when
  # a failure's diagnostics printed "no configuration file provided"
  # instead of the daemon logs.
  cd "$ROOT" || return
  docker compose logs runkod --tail 40 || true
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

START=$(date +%s)

echo "==> building CLIs"
BIN=$(mktemp -d)
go build -o "$BIN/runko" ./cmd/runko
go build -o "$BIN/runko-ci" ./cmd/runko-ci

echo "==> docker compose up"
docker compose up -d --build

echo "==> waiting for /readyz"
for i in $(seq 1 120); do
  if curl -fsS "$BASE_URL/readyz" >/dev/null 2>&1; then break; fi
  [ "$i" = 120 ] && { echo "daemon never became ready" >&2; exit 1; }
  sleep 2
done

WORK=$(mktemp -d)
echo "==> cloning (as alice)"
git clone "$(echo "$BASE_URL" | sed "s|http://|http://alice:$ALICE_TOKEN@|")/monorepo.git/" "$WORK/mono" 2>&1 | grep -v '^warning:' || true
cd "$WORK/mono"
git config user.name "alice"
git config user.email "alice@example.com"

land_change() { # $1 = change id
  local id=$1
  echo "==> report smoke-check green for $id"
  "$BIN/runko-ci" report-check --url "$BASE_URL/api/changes/$id/checks" \
    --token "$ALICE_TOKEN" --name smoke-check --external-id smoke-$RANDOM \
    --status completed --conclusion success --reporter compose-smoke

  echo "==> alice may not approve her own change (§8.7)"
  if "$BIN/runko" change approve --change "$id" --owner group:commerce-eng \
      --by alice --runkod-url "$BASE_URL" --token "$ALICE_TOKEN" 2>/dev/null; then
    echo "SELF-APPROVAL WAS ACCEPTED - the gate is broken" >&2; exit 1
  fi

  echo "==> bob approves"
  "$BIN/runko" change approve --change "$id" --owner group:commerce-eng \
    --by bob --runkod-url "$BASE_URL" --token "$BOB_TOKEN" >/dev/null

  echo "==> land"
  "$BIN/runko" change land --change "$id" --runkod-url "$BASE_URL" --token "$BOB_TOKEN"
}

echo "==> create project"
"$BIN/runko" project create --name checkout-api --type service --owners group:commerce-eng
ID1=$("$BIN/runko" change push --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["change_id"])')
land_change "$ID1"

echo "==> edit -> second change"
PROJECT_DIR=$(dirname "$(git ls-files | grep PROJECT.yaml | head -1)")
git fetch -q origin main && git reset -q --hard FETCH_HEAD
# APPEND to the template-generated main.go: the first smoke iteration
# wrote the template's exact bytes back ("nothing to commit" - the
# service template already ships main.go with an empty func main).
printf '\n// edited in the §16.4 eval loop\n' >> "$PROJECT_DIR/main.go"
git add -A && git commit -q -m "edit entrypoint"
ID2=$("$BIN/runko" change push --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["change_id"])')
land_change "$ID2"

echo "==> verify trunk"
git fetch -q origin main
COUNT=$(git rev-list --count origin/main)
[ "$COUNT" -ge 2 ] || { echo "expected >= 2 commits on trunk, got $COUNT" >&2; exit 1; }
LANDED=$("$BIN/runko" change list --state landed --runkod-url "$BASE_URL" --token "$ALICE_TOKEN" --json | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')
[ "$LANDED" = 2 ] || { echo "expected 2 landed changes, got $LANDED" >&2; exit 1; }

ELAPSED=$(( $(date +%s) - START ))
echo "==> eval loop completed in ${ELAPSED}s (budget ${BUDGET_SECONDS}s, §3.3/§16.4)"
[ "$ELAPSED" -lt "$BUDGET_SECONDS" ] || { echo "OVER BUDGET" >&2; exit 1; }
