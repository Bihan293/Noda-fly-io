# ============================================================
# Noda — Multi-stage Docker build
# ============================================================
# Build:   docker build -t noda .
# Run:     docker run -p 3000:3000 -p 9333:9333 noda
# ============================================================

# ---------- Stage 1: Build ----------
FROM golang:1.22-alpine AS builder

# Install git (needed for Go modules with VCS info) and ca-certs.
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Cache dependencies first (layer caching optimization).
COPY go.mod ./
RUN go mod download

# Copy source and build a static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /bin/noda .

# ---------- Stage 2: Runtime ----------
FROM alpine:3.19

# Add ca-certs for HTTPS peer communication and create non-root user.
RUN apk add --no-cache ca-certificates \
    && adduser -D -h /app noda

WORKDIR /app

# Copy the compiled binary from builder.
COPY --from=builder /bin/noda /app/noda

# Own everything by the non-root user.
RUN chown -R noda:noda /app
USER noda

# Default environment variables (override at runtime).
ENV PORT=3000
ENV P2P_PORT=9333
ENV DATA_FILE=/app/node_data.json
ENV LOG_LEVEL=info
ENV RATE_LIMIT=10

# Expose HTTP and P2P ports.
EXPOSE 3000 9333

# Health check — hit the /health endpoint.
HEALTHCHECK --interval=30s --timeout=3s --start_period=5s --retries=3 \
    CMD wget -qO- http://localhost:${PORT}/health || exit 1

# Run the node.
ENTRYPOINT ["/app/noda"]
