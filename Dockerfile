# syntax=docker/dockerfile:1
# --- build stage ---
FROM golang:1.25-alpine@sha256:REPLACE AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/luks-csi ./cmd/luks-csi

# --- controller runtime: distroless, non-root ---
FROM gcr.io/distroless/static-debian12:nonroot@sha256:REPLACE AS controller
COPY --from=build /out/luks-csi /usr/local/bin/luks-csi
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/luks-csi"]

# --- node runtime: needs cryptsetup + mount ---
FROM debian:12-slim@sha256:REPLACE AS node
RUN apt-get update \
 && apt-get install -y --no-install-recommends cryptsetup-bin e2fsprogs \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/luks-csi /usr/local/bin/luks-csi
ENTRYPOINT ["/usr/local/bin/luks-csi"]
