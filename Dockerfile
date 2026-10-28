# syntax=docker/dockerfile:1

# ---------- build ----------
FROM golang:1.26-alpine AS builder

WORKDIR /src

# Dependencies are copied and downloaded first so that this layer is reused
# whenever only application code changes.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG VERSION=dev
# CGO off plus -trimpath gives a static, reproducible binary that runs on an
# empty base image; -s -w drops the symbol table and DWARF data.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/server ./cmd/server

# ---------- health probe ----------
# A shell-less runtime image has no way to call a gRPC endpoint, so the
# standard probe is built here and copied in. The version is pinned.
FROM golang:1.26-alpine AS probe

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go install github.com/grpc-ecosystem/grpc-health-probe@v0.4.60

# ---------- runtime ----------
# distroless/static: no shell, no package manager, no libc. Minimal attack
# surface, and :nonroot runs as uid 65532 by default.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/server /usr/local/bin/server
COPY --from=probe /go/bin/grpc-health-probe /usr/local/bin/grpc-health-probe

USER nonroot:nonroot
EXPOSE 50051

ENTRYPOINT ["/usr/local/bin/server"]
