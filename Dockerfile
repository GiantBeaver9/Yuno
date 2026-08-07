# Multi-stage build producing the single self-contained yuno binary: build the
# React SPA, embed it, compile a CGO-free Go binary. Ported from pi-server.
#   docker build -t yuno .

# --- Stage 1: build the React SPA into web/dist ---
FROM node:22-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install
COPY frontend/ ./
# vite.config.ts writes the build into ../web/dist.
RUN npm run build

# --- Stage 2: compile the Go binaries (backend + MCP servers) with SPA embedded ---
FROM golang:1.25-bookworm AS backend
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# Overlay the freshly built SPA so //go:embed all:dist has real content.
COPY --from=frontend /app/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /out/yuno ./cmd/yuno \
 && CGO_ENABLED=0 go build -o /out/yuno-memory ./cmd/mcp-memory \
 && CGO_ENABLED=0 go build -o /out/yuno-create-agent ./cmd/mcp-create-agent

# --- Stage 3: runtime (glibc base — the goose binary is glibc, not musl) ---
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl bzip2 git \
 && rm -rf /var/lib/apt/lists/*
# Install the Goose CLI so agent turns can actually execute. Pinned to the stable
# channel; remove this block if you mount an external goose instead.
RUN curl -fsSL https://raw.githubusercontent.com/block/goose/main/download_cli.sh \
      | CONFIGURE=false GOOSE_BIN_DIR=/usr/local/bin bash \
 && goose --version
RUN useradd -m -u 10001 app && mkdir -p /app/workspaces /app/agents /app/bin && chown -R app:app /app
WORKDIR /app
COPY --from=backend /out/yuno /usr/local/bin/yuno
COPY --from=backend /out/yuno-memory /out/yuno-create-agent /app/bin/
USER app
ENV PORT=8080 \
    GOOSE_PATH=goose \
    MCP_BIN_DIR=/app/bin \
    AGENTS_DIR=/app/agents
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/yuno"]
