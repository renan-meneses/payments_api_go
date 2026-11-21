# syntax=docker/dockerfile:1.7

# ---------------------------------------------------------------------------
# Build stage
# ---------------------------------------------------------------------------
FROM golang:1.24-alpine AS build

WORKDIR /src

RUN apk add --no-cache git ca-certificates

# Dependency layer: cached unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/events-consumer ./cmd/events-consumer

# ---------------------------------------------------------------------------
# Runtime stage
# Alpine is used (instead of distroless) so docker-compose healthchecks can
# use wget. Harden further with distroless/static in production.
# ---------------------------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app && adduser -S -G app app

WORKDIR /app

COPY --from=build /out/api /app/api
COPY --from=build /out/events-consumer /app/events-consumer

USER app

EXPOSE 8080

ENTRYPOINT ["/app/api"]
