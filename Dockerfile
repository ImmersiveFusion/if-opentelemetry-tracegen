# Container image for TraceGen (the OTLP trace generator).
# Multi-stage: build the static Go binary, then ship it on a distroless base.
# Built + pushed to Docker Hub (immersivefusion/tracegen) by .github/workflows/release.yml
# on a version-tag push. Multi-arch via buildx (TARGETOS/TARGETARCH).
#
# Usage on a cluster (the binary takes the same flags as the CLI):
#   docker run --rm immersivefusion/tracegen -endpoint <collector:4317> -insecure ...
# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /out/tracegen ./cmd/tracegen

# distroless/static: no shell, CA certs included (for OTLP/TLS egress), runs as non-root.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tracegen /usr/bin/tracegen
# Containers run permanently (the free demo grids), so default to errors-only: the
# per-tick "N traces sent" heartbeat is pure log noise/cost at that scale. The bare
# CLI default stays "info" (unchanged). Override per-deployment with
# -e TRACEGEN_LOG_LEVEL=info|debug|silent, or pass -log-level / -quiet.
ENV TRACEGEN_LOG_LEVEL=error
ENTRYPOINT ["/usr/bin/tracegen"]
