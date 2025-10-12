# syntax=docker/dockerfile:1

ARG GO_VERSION=1.25
FROM golang:${GO_VERSION}-bookworm AS builder

WORKDIR /src
ENV CGO_ENABLED=0 \
    GOWORK=off

# Cache dependencies
COPY apps/api/go.mod apps/api/go.sum ./apps/api/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd apps/api && go mod download

# Copy sources
COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd apps/api && \
    go build -o /out/api ./cmd/api && \
    go build -o /out/migrate ./cmd/migrate && \
    go build -o /out/cron ./cmd/cron

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/api /app/api
COPY --from=builder /out/migrate /app/migrate
COPY --from=builder /out/cron /app/cron
EXPOSE 8080
ENTRYPOINT ["/app/api"]
