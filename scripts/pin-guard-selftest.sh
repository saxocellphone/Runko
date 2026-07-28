#!/usr/bin/env bash
# Self-test for the GitOps pin guard in .github/workflows/release-images.yml -
# the one piece of CD logic that runs ONLY in CI, against a repo we cannot
# dry-run, and whose failure mode is silent (a digest that never reaches git).
# It landed a stranded runkod image on 2026-07-28, so it gets a test.
#
# The script under test is EXTRACTED from the workflow, never copied: this
# cannot drift from what CI runs. It is exercised against a throwaway monorepo
# (A -> B -> C) and a throwaway GitOps repo with a real bare origin, covering
# the four states the per-image ancestry guard distinguishes:
#
#   1. legacy single-sha .pinned-from, this run a descendant -> pin all,
#      upgrade the file to per-image provenance
#   2. one image pinned from a NEWER land -> leave that one, pin the rest
#      (the incident: a web-only land stranding runkod's digest)
#   3. every built image already pinned from a newer land -> no-op
#   4. no .pinned-from at all -> fail open and pin
#
# Manual (not a gating check): it needs `yq` and network-free git, and CI
# already runs the real thing. Run it when touching the pin job:
#
#   scripts/pin-guard-selftest.sh
set -euo pipefail

cd "$(dirname "$0")/.."
WF=.github/workflows/release-images.yml
command -v yq >/dev/null || { echo "pin-guard-selftest: needs yq (go install github.com/mikefarah/yq/v4@latest)"; exit 2; }

ROOT=$(mktemp -d); trap 'rm -rf "$ROOT"' EXIT
SCRIPT="$ROOT/pin.sh"
python3 - "$WF" "$SCRIPT" <<'PY'
import sys, yaml
wf = yaml.safe_load(open(sys.argv[1]))
step = next(s for s in wf["jobs"]["pin"]["steps"]
            if s.get("name", "").startswith("pin the built digests"))
open(sys.argv[2], "w").write(
    step["run"].replace("${{ steps.app-token.outputs.app-slug }}", "pinbot"))
PY

MONO="$ROOT/mono"; git init -q "$MONO"
git -C "$MONO" config user.email t@t; git -C "$MONO" config user.name t
for c in A B C; do echo "$c" > "$MONO/f"; git -C "$MONO" add -A; git -C "$MONO" commit -qm "$c"; done
SHA_A=$(git -C "$MONO" rev-parse HEAD~2)
SHA_B=$(git -C "$MONO" rev-parse HEAD~1)
SHA_C=$(git -C "$MONO" rev-parse HEAD)

KUSTREL="apps/platform/kustomization.yaml"
OLD_RUNKOD="sha256:$(printf '0%.0s' {1..64})"
OLD_WEB="sha256:$(printf '1%.0s' {1..64})"
NEW_RUNKOD="sha256:$(printf 'a%.0s' {1..64})"
NEW_WEB="sha256:$(printf 'b%.0s' {1..64})"

fails=0
# run_case <name> <pinned-from content> <head sha> <report specs...>
# Leaves the GitOps working tree at $CASE for the caller to assert on.
run_case() {
  local name="$1" pinfrom="$2" head="$3"; shift 3
  local bare="$ROOT/$name.git" work="$ROOT/$name-gitops" ws="$ROOT/$name-ws"
  git init -q --bare "$bare"; git init -q "$work"
  git -C "$work" config user.email t@t; git -C "$work" config user.name t
  mkdir -p "$work/$(dirname "$KUSTREL")"
  cat > "$work/$KUSTREL" <<EOF
images:
  - name: reg/runkod
    digest: $OLD_RUNKOD
  - name: reg/web
    digest: $OLD_WEB
EOF
  [ -n "$pinfrom" ] && printf '%s' "$pinfrom" > "$work/apps/platform/.pinned-from"
  git -C "$work" add -A; git -C "$work" commit -qm seed
  git -C "$work" remote add origin "$bare"; git -C "$work" push -q origin HEAD:main

  mkdir -p "$ws"; cp -r "$MONO/.git" "$ws/.git"; ln -s "$work" "$ws/gitops"
  for spec in "$@"; do
    IFS=: read -r n r d <<<"$spec"
    mkdir -p "$ws/reports/report-$n"; printf '%s %s' "$r" "$d" > "$ws/reports/report-$n/$n"
  done
  ( cd "$ws" && GITHUB_WORKSPACE="$ws" GITHUB_REPOSITORY="acme/Runko" \
      HEAD_SHA="$head" KUST="$KUSTREL" bash "$SCRIPT" ) > "$ROOT/$name.log" 2>&1 \
    || { echo "FAIL $name: pin script exited $?"; sed 's/^/    /' "$ROOT/$name.log"; fails=$((fails+1)); }
  CASE="$work"
}

# want <case> <image ref> <digest> - assert the kustomization pin
want() {
  local got; got=$(yq -r ".images[] | select(.name == \"$2\").digest" "$CASE/$KUSTREL")
  [ "$got" = "$3" ] || { echo "FAIL $1: $2 is $got, want $3"; fails=$((fails+1)); }
}
# want_provenance <case> <sha> <image ref>
want_provenance() {
  grep -qx "$2 $3" "$CASE/apps/platform/.pinned-from" \
    || { echo "FAIL $1: .pinned-from lacks '$2 $3':"; sed 's/^/    /' "$CASE/apps/platform/.pinned-from"; fails=$((fails+1)); }
}

# 1. legacy file, descendant head: everything pins, provenance upgrades.
run_case legacy "$SHA_A"$'\n' "$SHA_C" "runkod:reg/runkod:$NEW_RUNKOD" "web:reg/web:$NEW_WEB"
want legacy reg/runkod "$NEW_RUNKOD"; want legacy reg/web "$NEW_WEB"
want_provenance legacy "$SHA_C" reg/runkod; want_provenance legacy "$SHA_C" reg/web

# 2. THE INCIDENT: web pinned from a newer land, this (older) run carries
# runkod. runkod must pin; web must keep both its digest and its provenance.
run_case stranded "$SHA_C reg/web"$'\n'"$SHA_A reg/runkod"$'\n' "$SHA_B" "runkod:reg/runkod:$NEW_RUNKOD"
want stranded reg/runkod "$NEW_RUNKOD"; want stranded reg/web "$OLD_WEB"
want_provenance stranded "$SHA_B" reg/runkod; want_provenance stranded "$SHA_C" reg/web

# 3. everything already pinned from a newer land: nothing moves.
run_case allnewer "$SHA_C reg/web"$'\n'"$SHA_C reg/runkod"$'\n' "$SHA_A" "runkod:reg/runkod:$NEW_RUNKOD"
want allnewer reg/runkod "$OLD_RUNKOD"
grep -q "nothing to do" "$ROOT/allnewer.log" || { echo "FAIL allnewer: expected the no-op notice"; fails=$((fails+1)); }

# 4. no provenance recorded: fail open rather than block a deploy.
run_case noprov "" "$SHA_B" "runkod:reg/runkod:$NEW_RUNKOD"
want noprov reg/runkod "$NEW_RUNKOD"; want_provenance noprov "$SHA_B" reg/runkod

[ "$fails" -eq 0 ] && echo "pin-guard-selftest: all 4 cases pass" || { echo "pin-guard-selftest: $fails failure(s)"; exit 1; }
