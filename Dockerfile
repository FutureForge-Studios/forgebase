# Reproducible build of the pgforged control-plane binary.
#
# This builds the binary ONLY. ForgeBase's runtime is host-coupled (pgforged
# shells out to `docker`, writes host paths under /opt/pgforge-*, manages systemd
# timers, spawns PostgREST/Deno, rewrites the pgbouncer userlist, and drives the
# firewall), so the supported way to RUN it is `install.sh` on a host, not a
# container. Use this image for CI / cross-compiling a clean binary:
#
#   docker build -t forgebase-build .
#   docker create --name fb forgebase-build && docker cp fb:/pgforged ./pgforged && docker rm fb
#
# Then drop ./pgforged onto the server as /opt/pgforge/bin/pgforged.
#
# Build args let the panel footer show the running commit (matches deploy.sh).
FROM golang:1.23-alpine AS build
ARG PGFORGE_VERSION=dev
ARG PGFORGE_BUILDTIME=unknown
WORKDIR /src
COPY pgforge/go.mod pgforge/go.sum ./
RUN go mod download
COPY pgforge/ ./
RUN CGO_ENABLED=0 go build \
      -ldflags "-s -w -X main.version=${PGFORGE_VERSION} -X main.buildTime=${PGFORGE_BUILDTIME}" \
      -o /pgforged .

# Minimal final stage so `docker cp` (or a scratch run) gets just the static binary.
FROM scratch
COPY --from=build /pgforged /pgforged
