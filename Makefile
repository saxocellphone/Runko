.PHONY: check fmt vet test build

check: fmt vet test

fmt:
	gofmt -l . | (! grep .)

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./...
