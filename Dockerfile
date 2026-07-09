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
RUN CGO_ENABLED=0 go build -o /out/runkod ./runkod/cmd/runkod \
    && CGO_ENABLED=0 go build -o /out/runko ./cli/runko \
    && CGO_ENABLED=0 go build -o /out/runko-ci ./cli/runko-ci \
    && CGO_ENABLED=0 go build -o /out/runko-bridge ./runkod/cmd/runko-bridge
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/zricethezav/gitleaks/v8@v8.21.2
# zoekt-git-index: lets the daemon's ZoektIndexWorker (§28.3 stage 11)
# index trunk on advance. Pinned to the SAME zoekt build the k8s
# deployment's zoekt-webserver runs (ghcr.io/sourcegraph/zoekt tag) -
# indexer and webserver must agree on shard format.
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/sourcegraph/zoekt/cmd/zoekt-git-index@v0.0.0-20260622122048-f80c7e09ab9d

FROM alpine:3.21
# git: the only substrate (§11). git-daemon is NOT optional: alpine
# packages git-http-backend there, not in the base git package, and
# Server.Handler() refuses to start without it (smart-HTTP is the only
# write path). wget (busybox) backs the compose healthcheck.
RUN apk add --no-cache git git-daemon ca-certificates
COPY --from=build /out/runkod /out/runko /out/runko-ci /out/runko-bridge /out/gitleaks /out/zoekt-git-index /usr/local/bin/
# The daemon makes commits during land (rebase) - give the process a git
# identity so machine-generated commits never fail on a bare container.
RUN git config --system user.name "runkod" && git config --system user.email "runkod@localhost" \
    && git config --system --add safe.directory '*'
EXPOSE 8080
ENTRYPOINT ["runkod"]
CMD ["serve"]
