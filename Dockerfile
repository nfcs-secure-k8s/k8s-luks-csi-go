# syntax=docker/dockerfile:1
# --- build stage ---
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG DEBUG_BUILD=false
RUN if [ "$DEBUG_BUILD" = "true" ]; then \
      CGO_ENABLED=0 go build -tags debug -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/luks-csi ./cmd/luks-csi; \
    else \
      CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/luks-csi ./cmd/luks-csi; \
    fi

# --- controller runtime: distroless, non-root ---
FROM gcr.io/distroless/static-debian12:nonroot AS controller
COPY --from=build /out/luks-csi /usr/local/bin/luks-csi
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/luks-csi"]

# --- node runtime: needs cryptsetup + mount ---
FROM debian:12-slim AS node
RUN apt-get update \
 && apt-get install -y --no-install-recommends cryptsetup-bin e2fsprogs \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/luks-csi /usr/local/bin/luks-csi
ENTRYPOINT ["/usr/local/bin/luks-csi"]
