# ==============================================================================
# Stage 1: Build the frontend (Vue 3 + Tailwind CSS with Bun)
# ==============================================================================
FROM oven/bun:1-alpine AS frontend-builder
WORKDIR /app/web

# Install frontend dependencies with lockfile caching
COPY web/package.json web/bun.lock* ./
RUN bun install --frozen-lockfile || bun install

# Copy frontend source code and compile production assets
COPY web/ ./
RUN bun run build

# ==============================================================================
# Stage 2: Build the Go backend binary (with embedded frontend)
# ==============================================================================
FROM golang:alpine AS backend-builder
WORKDIR /app

RUN apk add --no-cache ca-certificates git

# Cache Go modules
COPY go.mod go.sum ./
RUN go mod download

# Copy application source code
COPY . .

# Copy compiled frontend from Stage 1 into web/dist for go:embed
COPY --from=frontend-builder /app/web/dist ./web/dist

# Build fully static binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -extldflags '-static'" \
    -o /app/talosdeck \
    ./cmd/talosdeck

# ==============================================================================
# Stage 3: Minimal runtime image (Alpine Linux)
# ==============================================================================
FROM alpine:latest

# Install minimal certificates for TLS/mTLS gRPC connections to Talos API
RUN apk --no-cache add ca-certificates tzdata && \
    addgroup -S talosdeck -g 1000 && \
    adduser -S talosdeck -u 1000 -G talosdeck && \
    mkdir -p /app/data && chown -R talosdeck:talosdeck /app/data

WORKDIR /app

# Copy compiled binary from builder
COPY --from=backend-builder --chown=talosdeck:talosdeck /app/talosdeck /app/talosdeck

# Run as unprivileged user
USER talosdeck:talosdeck

# Expose HTTP port
EXPOSE 8080

# Runtime configuration defaults
ENV PORT=":8080" \
    TALOSCONFIG="/etc/talos/talosconfig"

ENTRYPOINT ["/app/talosdeck"]
