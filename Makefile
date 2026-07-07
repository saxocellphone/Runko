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
check-db:
	@if [ -z "$$RUNKO_TEST_DATABASE_URL" ]; then \
		echo "RUNKO_TEST_DATABASE_URL is not set - see db/README.md"; \
		exit 1; \
	fi
	go test ./... -run Postgres -v
