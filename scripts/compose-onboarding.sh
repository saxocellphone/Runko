#!/usr/bin/env bash
# Onboarding journey over the real compose stack (§6.10) - the 2026-07-16
# dogfood review, replayed as CI: a new human with nothing but the CLI and
# a host URL signs up, gets a genesis-seeded org, lands work alone on day
# one, brings an agent along, and every documented refusal answers with
# its structured text. docs/smoke-plan.md's "Onboarding journey" section
# is the row-by-row spec (O-rows / R-O-rows).
#
# Runs after scripts/compose-edgecases.sh in CI (same job, image layers
# warm). Own stack lifecycle, fresh volumes, signup + org-create enabled -
# the other suites never see those surfaces on.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)

BASE_URL=${BASE_URL:-http://localhost:8080}
HOST_PORT=${BASE_URL#http://}

# The stack this suite needs: self-service onboarding on, one admin
# principal for the operator moves (force-land, member promotion), and
# NO global checks - the day-one claim is that owners alone gate a fresh
# org (uploader consent), not that CI happens to be green.
export RUNKO_ALLOW_SIGNUP=true
export RUNKO_ALLOW_ORG_CREATE=true
export RUNKO_GLOBAL_REQUIRED_CHECKS=
export RUNKO_PRINCIPALS='name=op;token=op-dev-token;admin'
export RUNKO_TOKEN=runko-dev-token
OP=op-dev-token
ANON=$RUNKO_TOKEN

step() { echo; echo "=== $1"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

# ONBOARDING_EXTERNAL_STACK=1: drive an already-running daemon at BASE_URL
# instead of managing a compose stack - the no-docker local loop (e.g. an
# in-memory `runkod serve` on a laptop or sandbox). CI leaves it unset.
EXTERNAL=${ONBOARDING_EXTERNAL_STACK:-}

cleanup() {
  cd "$ROOT" || return
  [ -n "$EXTERNAL" ] && return
  docker compose logs runkod --tail 60 || true
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

CAPTURED=""
expect_fail() { # <desc> <cmd...>: MUST fail; output lands in $CAPTURED
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

jget() { # <python expr over j> (json on stdin)
  python3 -c "import json,sys; j=json.load(sys.stdin); print($1)"
}

# Personas: stored logins and managed workspace homes are part of what is
# under test, so each actor gets an isolated XDG_CONFIG_HOME +
# RUNKO_WORKSPACE_HOME - nothing leaks between actors or into the
# runner's real HOME.
PERSONAS=$(mktemp -d)
as_persona() { # <persona> <cmd...>
  local who=$1; shift
  mkdir -p "$PERSONAS/$who/config" "$PERSONAS/$who/ws"
  # -u RUNKO_TOKEN/-u RUNKO_RUNKOD_URL: those exports exist for compose
  # interpolation, but the CLI's git auth gives ambient env precedence
  # over the stored login (gitauth.go's hook fallback) - leaking them in
  # made every persona push as the ANONYMOUS deploy token, authored_by
  # came back empty, and uploader consent never fired (found on this
  # suite's first real run). A real user has no ambient tokens; neither
  # do personas.
  env -u RUNKO_TOKEN -u RUNKO_RUNKOD_URL \
    XDG_CONFIG_HOME="$PERSONAS/$who/config" RUNKO_WORKSPACE_HOME="$PERSONAS/$who/ws" "$@"
}
as_val()    { as_persona val    "$@"; }
as_worker() { as_persona worker "$@"; }
as_agent()  { as_persona agent  "$@"; }
# agent_push_in <dir>: an agent change push from inside a worktree -
# named so expect_fail can run it (cd in a function, not a subshell trick).
agent_push_in() { (cd "$1" && as_agent "$RUNKO" change push); }

step "build CLI + fresh stack (signup + org-create enabled)"
BIN=$(mktemp -d)
go build -o "$BIN/runko" ./cli/runko
RUNKO="$BIN/runko"
# Worktrees stamp this binary's ABSOLUTE path as their credential helper
# (gitauth.go os.Executable), but keep it on PATH too for anything that
# shells out by name.
export PATH="$BIN:$PATH"
[ -z "$EXTERNAL" ] && docker compose up -d --build
for i in $(seq 1 120); do
  curl -fsS "$BASE_URL/readyz" >/dev/null 2>&1 && break
  [ "$i" = 120 ] && fail "daemon never became ready"
  sleep 2
done

# ---------------------------------------------------------------- O1
step "O1: first contact - signup creates the account, the org, and the login"
as_val "$RUNKO" auth signup --runkod-url "$BASE_URL" --name val --password valpass123 --org acme --create
as_val "$RUNKO" auth status | grep -q "as val" || fail "O1: auth status does not show val"
as_val "$RUNKO" org list --json | jget '[o["name"] for o in j if o["role"]=="admin"]' | grep -q acme \
  || fail "O1: org list does not show acme with role admin"

# ---------------------------------------------------------------- O2
step "O2: genesis over the wire - the org is born usable"
CLONES=$(mktemp -d)
git clone -q "http://val:valpass123@$HOST_PORT/o/acme/acme.git" "$CLONES/acme"
grep -q val "$CLONES/acme/OWNERS" || fail "O2: genesis OWNERS does not name the creator"
for f in AGENTS.md CONTRIBUTING.md PROJECT.yaml .claude/skills/runko/SKILL.md; do
  [ -f "$CLONES/acme/$f" ] || fail "O2: genesis tree lacks $f"
done

# ---------------------------------------------------------------- O3
step "O3: workspace create with no flags beyond the name - fully wired"
WS_JSON=$(as_val "$RUNKO" workspace create --name onboard --project repo --json)
WS=$(echo "$WS_JSON" | jget 'j["Dir"]')
[ -d "$WS" ] || fail "O3: workspace dir $WS does not exist"
DOC=$(cd "$WS" && as_val "$RUNKO" doctor --json)
echo "$DOC" | jget 'j["HasChangeIDHook"] and j["HasVerbNudgeHook"]' | grep -q True \
  || fail "O3: materialized checkout is missing client hooks"
echo "$DOC" | jget 'j["CLI"]["go"]' | grep -q go || fail "O3: doctor does not report the CLI build identity"
as_val "$RUNKO" version --json | jget 'j["go"]' | grep -q go || fail "O3: runko version is silent"

# ---------------------------------------------------------------- O4
step "O4: first project lands by its author alone (genesis + uploader consent)"
CREATE_JSON=$(cd "$WS" && as_val "$RUNKO" project create --name hello --type app --lang ts --api rest --build-engine vite --json)
CH=$(echo "$CREATE_JSON" | jget 'j["change_id"]')
[ -n "$CH" ] || fail "O4: project create minted no change_id"
git -C "$WS" log -1 --format=%B | grep -q "Change-Id: $CH" || fail "O4: create commit lacks its Change-Id trailer"
[ -f "$WS/hello/openapi.yaml" ] || fail "O4: rest scaffold missing openapi.yaml"
(cd "$WS" && as_val "$RUNKO" change push)
REQ=$(cd "$WS" && as_val "$RUNKO" change requirements --change "$CH" 2>&1) || true
echo "$REQ" | grep -q "ready to land" \
  || { echo "$REQ"; fail "O4: the creator's own change is not immediately landable"; }
(cd "$WS" && as_val "$RUNKO" change land --change "$CH") || fail "O4: land failed"

# ---------------------------------------------------------------- O5
step "O5: the steady-state loop (edit -> change -> push -> land)"
(cd "$WS" && as_val "$RUNKO" workspace sync)
echo "note" > "$WS/hello/NOTES.md"
(cd "$WS" && as_val "$RUNKO" change create -m "hello: add notes")
(cd "$WS" && as_val "$RUNKO" change push)
CH2=$(git -C "$WS" log -1 --format=%B | grep -o 'I[0-9a-f]\{40\}' | head -1)
(cd "$WS" && as_val "$RUNKO" change land --change "$CH2") || fail "O5: steady-state land failed"

# ---------------------------------------------------------------- O6
step "O6: greenfield - a workspace for a project that is not on trunk yet"
GF_JSON=$(as_val "$RUNKO" workspace create --name greenfield --new-path services/checkout --json)
GF=$(echo "$GF_JSON" | jget 'j["Dir"]')
echo "$GF_JSON" | jget '"services/checkout" in j["WriteAllowlist"]' | grep -q True \
  || fail "O6: new path missing from the write allowlist"
(cd "$GF" && as_val "$RUNKO" project create --name checkout --type service --path services/checkout --lang ts --api rest --build-engine vite)
(cd "$GF" && as_val "$RUNKO" change push)
GCH=$(git -C "$GF" log -1 --format=%B | grep -o 'I[0-9a-f]\{40\}' | head -1)
(cd "$GF" && as_val "$RUNKO" change land --change "$GCH") || fail "O6: greenfield land failed"

step "R-O5: --new-path refusals"
expect_fail "R-O5 escape" as_val "$RUNKO" workspace create --name bad1 --new-path ../escape
expect_in "not a clean repo-relative directory path" "R-O5 escape"
expect_fail "R-O5 collision" as_val "$RUNKO" workspace create --name bad2 --new-path hello
expect_in "is already project" "R-O5 collision"

# ---------------------------------------------------------------- O7
step "O7: the agent lifecycle - mint, work, describe, automerge on approval"
AGENT_JSON=$(as_val "$RUNKO" agent create --task greet --json)
AGENT=$(echo "$AGENT_JSON" | jget 'j["name"]')
AGENT_TOKEN=$(echo "$AGENT_JSON" | jget 'j["token"]')
as_agent "$RUNKO" auth login --runkod-url "$BASE_URL/o/acme" --name "$AGENT" --token "$AGENT_TOKEN"
AWS_JSON=$(as_agent "$RUNKO" workspace create --name greet --project hello --new-path services/agentproj --json)
AWS=$(echo "$AWS_JSON" | jget 'j["Dir"]')
echo "hello from $AGENT" > "$AWS/hello/GREETING.md"
(cd "$AWS" && as_agent "$RUNKO" change create -m "hello: greeting")
(cd "$AWS" && as_agent "$RUNKO" change push)
ACH=$(git -C "$AWS" log -1 --format=%B | grep -o 'I[0-9a-f]\{40\}' | head -1)
(cd "$AWS" && as_agent "$RUNKO" change requirements --change "$ACH") | grep -qi "description" \
  || fail "O7: RequireDescription blocker missing for an undescribed agent change"
as_agent "$RUNKO" change describe --change "$ACH" --description "Adds a greeting note to hello (onboarding smoke O7)."
as_agent "$RUNKO" change automerge --change "$ACH"
as_val "$RUNKO" change approve --change "$ACH" --owner val --by val
for i in $(seq 1 30); do
  as_val "$RUNKO" change list --state landed 2>/dev/null | grep -q "$ACH" && break
  [ "$i" = 30 ] && fail "O7: automerge never landed the approved agent change"
  sleep 2
done

# ---------------------------------------------------------------- R-O1..R-O3 + O8
step "R-O1: an agent editing an existing manifest is refused"
# O7's land AUTO-CLOSED the greet workspace (single-use agent workspaces:
# one workspace = one task, §12.2/finding #42) - the policy probes are a
# new task and get a fresh one, exactly as the closed-workspace refusal
# teaches.
AWS_JSON=$(as_agent "$RUNKO" workspace create --name policy-probe --project hello --new-path services/agentproj --json)
AWS=$(echo "$AWS_JSON" | jget 'j["Dir"]')
printf '\n# poked by agent\n' >> "$AWS/hello/PROJECT.yaml"
(cd "$AWS" && as_agent "$RUNKO" change create -m "hello: poke manifest")
expect_fail "R-O1" agent_push_in "$AWS"
expect_in "does not allow modifying owners" "R-O1"
git -C "$AWS" reset -q --hard HEAD~1

step "R-O2 / O8: a new manifest naming the agent is refused; naming the human passes"
mkdir -p "$AWS/services/agentproj"
printf 'schema: project/v1\nname: agentproj\ntype: library\nowners:\n  - %s\n' "$AGENT" > "$AWS/services/agentproj/PROJECT.yaml"
(cd "$AWS" && as_agent "$RUNKO" change create -m "Create project agentproj")
expect_fail "R-O2" agent_push_in "$AWS"
expect_in "grants itself ownership" "R-O2"
# The refused commit is still the branch tip - drop it, or the corrected
# manifest would stack ON TOP of it and the whole-push policy would keep
# refusing the union.
git -C "$AWS" reset -q --hard HEAD~1
mkdir -p "$AWS/services/agentproj"
printf 'schema: project/v1\nname: agentproj\ntype: library\nowners:\n  - val\n' > "$AWS/services/agentproj/PROJECT.yaml"
(cd "$AWS" && as_agent "$RUNKO" change create -m "Create project agentproj, owned by val")
(cd "$AWS" && as_agent "$RUNKO" change push) || fail "O8: agent project create naming the human was refused"
OCH=$(git -C "$AWS" log -1 --format=%B | grep -o 'I[0-9a-f]\{40\}' | head -1)
as_agent "$RUNKO" change abandon --change "$OCH"

step "R-O3: product-lifecycle verbs stay human"
expect_fail "R-O3 delete" as_agent "$RUNKO" project delete --name hello
expect_in "human product action" "R-O3 delete"
# A minted agent's rows are ORG-scoped: at the hub root it cannot even
# authenticate (401, sign-in matrix R9) - a flag-config agent on the
# default org would get the structured agent_denied instead (R8). Either
# way: refused.
expect_fail "R-O3 org" as_agent "$RUNKO" org create --name agent-org

# ---------------------------------------------------------------- bare-org chain
step "R-O6/R-O7: the bare-org retrofit chain (org bootstrap end to end)"
"$RUNKO" org create --name bare --runkod-url "$BASE_URL" --token "$ANON" --no-switch
git clone -q "http://runko:$ANON@$HOST_PORT/o/bare/bare.git" "$CLONES/bare" 2>/dev/null || true
BARE_ID="Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
(cd "$CLONES/bare" \
  && mkdir -p tool && printf 'schema: project/v1\nname: tool\ntype: other\n' > tool/PROJECT.yaml \
  && git add -A \
  && git -c user.name=anon -c user.email=anon@eval commit -q -m "first project

Change-Id: $BARE_ID" \
  && git push -q origin "HEAD:refs/for/main")

step "R-O6: landing without governance names the escape hatch"
# The in-memory eval profile IMPLIES --insecure-allow-unpoliced-land
# (§9.3), so default-deny only exists on a durable (Postgres) stack -
# compose always is one; a local ONBOARDING_EXTERNAL_STACK daemon may
# not be. Detect which posture we are on instead of assuming.
REQ=$("$RUNKO" change requirements --change "$BARE_ID" --runkod-url "$BASE_URL/o/bare" --token "$OP" 2>&1) || true
if echo "$REQ" | grep -q "ready to land"; then
  echo "NOTE: eval profile (unpoliced lands allowed) - the default-deny blocker rows only assert on a durable stack"
  "$RUNKO" change land --change "$BARE_ID" --runkod-url "$BASE_URL/o/bare" --token "$OP" --sync=false \
    || fail "bare chain: eval-profile land failed"
else
  echo "$REQ" | grep -q "runko org bootstrap" || { echo "$REQ"; fail "R-O6: the unpoliced blocker does not name the escape hatch"; }
  expect_fail "R-O6 land" "$RUNKO" change land --change "$BARE_ID" --runkod-url "$BASE_URL/o/bare" --token "$OP" --sync=false
  "$RUNKO" change land --change "$BARE_ID" --runkod-url "$BASE_URL/o/bare" --token "$OP" --sync=false --force \
    || fail "bare chain: operator force-land failed"
fi

step "R-O7: bootstrap gates - anonymous, non-admin, then the real thing"
expect_fail "R-O7 anon" "$RUNKO" org bootstrap --runkod-url "$BASE_URL/o/bare" --token "$ANON"
expect_in "has no name to record" "R-O7 anon"
as_worker "$RUNKO" auth signup --runkod-url "$BASE_URL" --name worker --password workerpw1 --org bare --join
expect_fail "R-O7 member" as_worker "$RUNKO" org bootstrap
expect_in "org admins only" "R-O7 member"
"$RUNKO" org add-member --org bare --name worker --role admin --runkod-url "$BASE_URL" --token "$OP"
BOOT_JSON=$(as_worker "$RUNKO" org bootstrap --json)
BCH=$(echo "$BOOT_JSON" | jget 'j["change_id"]')
[ -n "$BCH" ] || fail "bare chain: bootstrap opened no change"
as_worker "$RUNKO" change land --change "$BCH" --sync=false || fail "bare chain: worker could not land their own bootstrap change"
git -C "$CLONES/bare" fetch -q origin main
git -C "$CLONES/bare" show origin/main:OWNERS | grep -q worker || fail "bare chain: trunk OWNERS does not name worker"

step "R-O7: bootstrap steps aside once governed"
expect_fail "R-O7 governed" as_worker "$RUNKO" org bootstrap
expect_in "owners already resolve" "R-O7 governed"

step "bare chain coda: ordinary work lands in the retrofitted org"
BWS_JSON=$(as_worker "$RUNKO" workspace create --name daily --project repo --json)
BWS=$(echo "$BWS_JSON" | jget 'j["Dir"]')
echo "retrofitted" > "$BWS/STATUS.md"
(cd "$BWS" && as_worker "$RUNKO" change create -m "status note")
(cd "$BWS" && as_worker "$RUNKO" change push)
WCH=$(git -C "$BWS" log -1 --format=%B | grep -o 'I[0-9a-f]\{40\}' | head -1)
(cd "$BWS" && as_worker "$RUNKO" change land --change "$WCH") || fail "bare chain coda: ordinary land failed"

# ---------------------------------------------------------------- R-O4, R-O8, R-O9
step "R-O4: wrong-org sign-in is 'not a member', never 'wrong password'"
expect_fail "R-O4" as_val "$RUNKO" auth login --runkod-url "$BASE_URL/o/bare" --name val --token valpass123
expect_in "not a member" "R-O4"

step "R-O8: duplicate org name"
expect_fail "R-O8" as_val "$RUNKO" org create --name acme
expect_in "already exists" "R-O8"

step "R-O9: workspace delete refuses while a change is open"
echo "wip" > "$WS/hello/WIP.md"
(cd "$WS" && as_val "$RUNKO" workspace sync && as_val "$RUNKO" change create -m "hello: wip")
(cd "$WS" && as_val "$RUNKO" change push)
WIPCH=$(git -C "$WS" log -1 --format=%B | grep -o 'I[0-9a-f]\{40\}' | head -1)
expect_fail "R-O9 open" as_val "$RUNKO" workspace delete onboard
as_val "$RUNKO" change abandon --change "$WIPCH"
as_val "$RUNKO" workspace delete onboard || fail "R-O9: delete still refused after abandon"

echo
echo "onboarding journey: all rows green"
