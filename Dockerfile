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

# --- Stage 2: compile the Go binary with the SPA embedded ---
FROM golang:1.25-alpine AS backend
WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# Overlay the freshly built SPA so //go:embed all:dist has real content.
COPY --from=frontend /app/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /out/yuno ./cmd/yuno

# --- Stage 3: minimal runtime ---
FROM alpine:3.20
RUN adduser -D -H -u 10001 app \
    && mkdir -p /app/workspaces /app/agents \
    && chown -R app:app /app
WORKDIR /app
COPY --from=backend /out/yuno /usr/local/bin/yuno
USER app
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/yuno"]
