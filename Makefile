.PHONY: check fmt vet test build check-db

check: fmt vet test

fmt:
	gofmt -l . | (! grep .)

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./...

# Runs the live-Postgres integration tests (internal/dbtest, docs/design.md
# §28.3 stage 9a item 1) that `test`/`check` skip when no database is
# configured. Requires RUNKO_TEST_DATABASE_URL (a Postgres the test may
# freely wipe - see db/README.md) and `psql` on PATH.
#
# -p 1: every *_pg_test.go package independently resets the WHOLE shared
# schema (internal/dbtest.Connect does DROP-then-CREATE) against the SAME
# database - `go test ./...` otherwise runs different packages' test
# binaries concurrently (bounded by GOMAXPROCS), so two packages' resets can
# interleave and race (one package's DROP mid-flight under another's
# already-running test, or two concurrent CREATE TABLE attempts). Harmless
# with few live-DB packages and easy to not notice locally; became a real,
# reproducible CI failure the moment a 4th and 5th package (runkod,
# cmd/runkod) gained *_pg_test.go files. -p 1 forces one package's test
# binary at a time, which is what these tests need given they share
# external, stateful infrastructure instead of being hermetic.
check-db:
	@if [ -z "$$RUNKO_TEST_DATABASE_URL" ]; then \
		echo "RUNKO_TEST_DATABASE_URL is not set - see db/README.md"; \
		exit 1; \
	fi
	go test ./... -run Postgres -v -p 1
