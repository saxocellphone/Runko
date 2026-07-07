# runkod image - the write-path daemon for the §9.3 "Eval / dev" compose
# profile (§16.4, §28.3 stage 14). Multi-stage: everything is built with Go
# modules (including gitleaks - pinned by version, reproducible,
# arch-independent; no release-tarball checksum dance), and the runtime
# layer is alpine for a git recent enough for the land engine
# (merge-tree --merge-base needs git >= 2.40, internal/gitversion).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/runkod ./cmd/runkod \
    && CGO_ENABLED=0 go build -o /out/runko ./cmd/runko \
    && CGO_ENABLED=0 go build -o /out/runko-ci ./cmd/runko-ci
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/zricethezav/gitleaks/v8@v8.21.2

FROM alpine:3.21
# git: the only substrate (§11); wget (busybox) backs the compose
# healthcheck against /readyz.
RUN apk add --no-cache git ca-certificates
COPY --from=build /out/runkod /out/runko /out/runko-ci /out/gitleaks /usr/local/bin/
# The daemon makes commits during land (rebase) - give the process a git
# identity so machine-generated commits never fail on a bare container.
RUN git config --system user.name "runkod" && git config --system user.email "runkod@localhost" \
    && git config --system --add safe.directory '*'
EXPOSE 8080
ENTRYPOINT ["runkod"]
CMD ["serve"]
