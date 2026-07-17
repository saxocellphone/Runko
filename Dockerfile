# runkod image - the write-path daemon for the §9.3 "Eval / dev" compose
# profile (§16.4, §28.3 stage 14). The runtime layer is alpine for a git
# recent enough for the land engine (merge-tree --merge-base needs
# git >= 2.40, internal/gitversion).
#
# TWO BUILD MODES (BUILD_MODE build-arg, buildkit resolves the stage):
#   source   (default) - self-contained `go build` from the context, used
#            by docker-compose / the §16.4 eval loop; needs nothing but
#            this repo.
#   prebuilt - the release path: CI bazel-builds the binaries (pure mode,
#            static - see .bazelrc `--config=release`) into prebuilt/ and
#            this file only assembles them, so image builds ride bazel's
#            incremental cache instead of recompiling the world.
# Both modes produce the same binary set; the external engines (gitleaks,
# zoekt) stay Go-module installs in their own stage - version-pinned,
# reproducible, arch-independent, and DECOUPLED from the source tree, so
# their layers cache until the pin changes instead of rebuilding on every
# landing.
ARG BUILD_MODE=source

FROM golang:1.25-alpine AS tools
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/zricethezav/gitleaks/v8@v8.21.2
# zoekt-git-index: lets the daemon's ZoektIndexWorker (§28.3 stage 11)
# index trunk on advance. Pinned to the SAME zoekt build the k8s
# deployment's zoekt-webserver runs (ghcr.io/sourcegraph/zoekt tag) -
# indexer and webserver must agree on shard format.
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/sourcegraph/zoekt/cmd/zoekt-git-index@v0.0.0-20260622122048-f80c7e09ab9d

FROM golang:1.25-alpine AS build-source
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/runkod ./runkod/cmd/runkod \
    && CGO_ENABLED=0 go build -o /out/runko ./cli/runko \
    && CGO_ENABLED=0 go build -o /out/runko-ci ./cli/runko-ci \
    && CGO_ENABLED=0 go build -o /out/runko-bridge ./runkod/cmd/runko-bridge \
    && CGO_ENABLED=0 go build -o /out/runko-watchdog ./watchdog \
    && CGO_ENABLED=0 go build -o /out/runko-mailer ./mailer \
    && CGO_ENABLED=0 go build -o /out/runko-deployer ./runkod/cmd/runko-deployer

# The release path drops bazel-built static binaries here (already named
# for the image: runko-watchdog, runko-mailer, ...) - see
# .github/workflows/release-images.yml.
FROM scratch AS build-prebuilt
COPY prebuilt/ /out/

FROM build-${BUILD_MODE} AS build

FROM alpine:3.21
# git: the only substrate (§11). git-daemon is NOT optional: alpine
# packages git-http-backend there, not in the base git package, and
# Server.Handler() refuses to start without it (smart-HTTP is the only
# write path). wget (busybox) backs the compose healthcheck.
RUN apk add --no-cache git git-daemon ca-certificates
COPY --from=tools /out/gitleaks /out/zoekt-git-index /usr/local/bin/
COPY --from=build /out/runkod /out/runko /out/runko-ci /out/runko-bridge /out/runko-watchdog /out/runko-mailer /out/runko-deployer /usr/local/bin/
# The daemon makes commits during land (rebase) - give the process a git
# identity so machine-generated commits never fail on a bare container.
RUN git config --system user.name "runkod" && git config --system user.email "runkod@localhost" \
    && git config --system --add safe.directory '*'
EXPOSE 8080
ENTRYPOINT ["runkod"]
CMD ["serve"]
