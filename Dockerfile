# ==============================================================================
# Stage 1: Build the frontend (Vue 3 + Tailwind CSS with Bun)
# ==============================================================================
FROM oven/bun:1.3.14-alpine AS frontend-builder
WORKDIR /app/web

# Install frontend dependencies with lockfile caching
COPY web/package.json web/bun.lock* ./
RUN bun install --frozen-lockfile

# Copy frontend source code and compile production assets
COPY web/ ./
RUN bun run build

# ==============================================================================
# Stage 2: Build the Go backend binary (with embedded frontend)
# ==============================================================================
FROM golang:1.27-alpine AS backend-builder
WORKDIR /app

ARG TARGETARCH
ARG TALOSCTL_VERSION=v1.14.0
RUN apk add --no-cache ca-certificates git wget \
    && if [ -z "${TARGETARCH}" ]; then \
         case "$(uname -m)" in x86_64) TARGETARCH=amd64 ;; aarch64) TARGETARCH=arm64 ;; esac; \
       fi \
    && case "${TARGETARCH}" in \
         amd64) TALOSCTL_SHA256=2c147c4a99d124c95bd5c190fe054e0b3c93495f2243fd652ebd423adb8377c7 ;; \
         arm64) TALOSCTL_SHA256=19615e1d0eb222de86ec2f1487e7d6e74f5171a9038e73aeacde8cc647e3d9e0 ;; \
         *) echo "Unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
       esac \
    && wget -q -O /app/talosctl "https://github.com/siderolabs/talos/releases/download/${TALOSCTL_VERSION}/talosctl-linux-${TARGETARCH}" \
    && echo "${TALOSCTL_SHA256}  /app/talosctl" | sha256sum -c - \
    && chmod 0755 /app/talosctl

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
FROM alpine:3.21.3

# Install minimal certificates for TLS/mTLS gRPC connections to Talos API
# and prepare data directories with proper permissions for unprivileged user
RUN apk --no-cache add ca-certificates tzdata openssh-client && \
    addgroup -S talosdeck -g 1000 && \
    adduser -S talosdeck -u 1000 -G talosdeck && \
    mkdir -p /app/data/backups && \
    chown -R talosdeck:talosdeck /app && \
    chmod -R 700 /app/data

WORKDIR /app

# Copy compiled binary from builder
COPY --from=backend-builder --chown=talosdeck:talosdeck /app/talosdeck /app/talosdeck
COPY --from=backend-builder --chown=talosdeck:talosdeck /app/talosctl /usr/local/bin/talosctl

# Expose HTTP port
EXPOSE 8080

# Persistent volume for application data (sqlite, audit logs, cluster backups)
VOLUME ["/app/data"]

# Run as unprivileged user
USER talosdeck:talosdeck

# Runtime configuration defaults
ENV PORT=":8080" \
    TALOSCONFIG="/etc/talos/talosconfig"

ENTRYPOINT ["/app/talosdeck"]
