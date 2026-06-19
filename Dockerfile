# syntax=docker/dockerfile:1
# Uses online tagged auth-client (go.mod replace => github.com/Bengo-Hub/auth-client v0.3.1).
# Build from repo root: docker build -f logistics-service/logistics-api/Dockerfile -t logistics-api:local .

FROM golang:1.24-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./

RUN GOTOOLCHAIN=auto go mod download
COPY . .

RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/logistics-api ./cmd/api
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/logistics-migrate ./cmd/migrate
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/logistics-seed ./cmd/seed

FROM alpine:3.20
# ca-certificates: TLS trust for HTTPS remotes (S3/OneDrive/GDrive/WebDAV).
# rclone: powers pluggable backup-destination mirroring off the local PVC.
RUN apk add --no-cache ca-certificates rclone
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app

# Binaries
COPY --from=builder /out/logistics-api /usr/local/bin/logistics-api
COPY --from=builder /out/logistics-migrate /usr/local/bin/logistics-migrate
COPY --from=builder /out/logistics-seed /usr/local/bin/logistics-seed

# Migrations and entrypoint
COPY internal/ent/migrate/migrations ./internal/ent/migrate/migrations
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Media folder setup
RUN mkdir -p /media && chown -R app:app /media && chmod -R 775 /media

USER app
EXPOSE 4000
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
