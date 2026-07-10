.PHONY: check fmt vet test build check-db check-race check-web check-bazel check-bazel-test check-bazel-race check-bazel-db check-docs

check: fmt vet test

fmt:
	gofmt -l . | (! grep .)

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./...

# Race-detector pass over the whole suite. The land engine's central
# guarantee (exactly one concurrent land wins, §13.5) and MemStore's
# concurrent-safety claim are only meaningful under -race - every
# implementation session runs this by hand, and CI runs it on every push so
# a regression can't slip through a session that forgot. Slower than `test`
# (the detector instruments every memory access), hence a separate target
# rather than part of `check`'s <30s budget (§28.2 rule 3).
check-race:
	go test -race ./...

# Runs the live-Postgres integration tests (internal/dbtest, docs/design.md
# §28.3 stage 9a item 1) that `test`/`check` skip when no database is
# configured. Requires RUNKO_TEST_DATABASE_URL (a Postgres the test may
# freely wipe - see db/README.md) and `psql` on PATH.
#
# Serialization: every *_pg_test.go package independently resets the WHOLE
# shared schema (internal/dbtest.Connect does DROP-then-CREATE) against the
# SAME database, while go/bazel both run test binaries concurrently. The
# harness self-serializes with a session-level Postgres advisory lock held
# per test (internal/dbtest), so no -p 1 / --local_test_jobs=1 runner flag
# is needed - pg tests are safe inside ANY test invocation, which is what
# lets the §14.9 per-project checks carry their own pg tests.
check-db:
	@if [ -z "$$RUNKO_TEST_DATABASE_URL" ]; then \
		echo "RUNKO_TEST_DATABASE_URL is not set - see db/README.md"; \
		exit 1; \
	fi
	go test ./... -run Postgres -v

# The content-tier check (§14.5.7): prose changes (markdown, LICENSE,
# images - the root manifest's `prose:` patterns) gate on this alone
# instead of the build graph. Needs only git + python3; seconds.
check-docs:
	scripts/check-docs.sh

# Frontend checks (web/): typecheck + lint + vitest + production build.
# Needs Node >= 22 (this sandbox: ~/.local/node/bin). Separate from
# `check` for the same reason as check-db: `check` stays the Go-only <30s
# loop (§28.2 rule 3); CI runs this as its own job.
check-web:
	cd web && npm install --no-audit --no-fund && npm run check

# Bazel graph health (docs/migration-findings.md, §14.5.4 dogfood): the
# graph must build, gazelle must not drift, and the rdeps recipe must work
# against the genuine engine. Tests are NOT run under bazel - `check` stays
# the test truth. Declared as the tree's `bazel-check` (PROJECT.yaml
# ci.checks on the Go projects), reported pre-land by runko-checks.yml.
# Needs bazel/bazelisk on PATH (this sandbox: ~/.local/bin).
check-bazel:
	bazel build //...
	bazel run //:gazelle
	git diff --exit-code -- '**/BUILD.bazel'
	go test -tags bazel_integration ./platform/buildadapter/bazel/

# The test suite under bazel (§14.5.4 golden path, adopted 2026-07-10:
# this repo is the reference implementation, so its checks run the way
# the spec tells customers to run theirs). The manifests' per-project
# checks (platform-test/-race, runkod-test/-race, cli-test,
# internal-test) run these same invocations scoped to their own
# subtrees; these whole-repo targets stay as the local convenience.
# `check` stays the fast plain-go inner loop - both runners exercise the
# same tests, and the e2e suites resolve their subject binaries from
# bazel runfiles with a `go build` fallback
# (runkod/cmd/runkod/main_test.go).
check-bazel-test:
	bazel test //...

check-bazel-race:
	bazel test --@rules_go//go/config:race //...

# The live-Postgres integration tests under bazel: env passthrough for the
# DSN, --test_filter narrows to the Postgres-gated tests (targets without
# any simply pass), and --nocache_test_results because a mutable external
# database is not a hermetic input. Cross-process serialization comes from
# the harness's own advisory lock (see check-db above), not a runner flag.
check-bazel-db:
	@if [ -z "$$RUNKO_TEST_DATABASE_URL" ]; then \
		echo "RUNKO_TEST_DATABASE_URL is not set - see db/README.md"; \
		exit 1; \
	fi
	bazel test --test_env=RUNKO_TEST_DATABASE_URL --test_filter=Postgres \
		--nocache_test_results //...

# The §16.4 measured eval loop (docker compose v2 + go required): compose
# up -> create -> change -> land, twice, timed against §3.3's budget. CI
# runs this on every push; this target is for anywhere Docker exists
# (NOT this repo's original sandbox - see CLAUDE.md).
check-compose:
	./scripts/compose-smoke.sh

# Edge-case suite over the compose stack (docs/smoke-plan.md): the
# invariants that only mean something through the full real transport.
# Runs after check-compose in CI; separate stack + volumes.
check-compose-edges:
	./scripts/compose-edgecases.sh
