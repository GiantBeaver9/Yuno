# Multi-stage build producing the single self-contained pi-dashboard binary,
# mirroring the Makefile (build the Vue SPA, embed it, compile a CGO-free Go
# binary). Build:  docker build -t pi-dashboard .
# Run:    docker run -p 8080:8080 -v pi-data:/data pi-dashboard

# --- Stage 1: build the Vue SPA ---
FROM node:20-alpine AS frontend
WORKDIR /app/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
# vite.config.ts writes the build into ../backend/web/dist.
RUN npm run build

# --- Stage 2: compile the Go binary with the SPA embedded ---
FROM golang:1.25-alpine AS backend
WORKDIR /app/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Overlay the freshly built SPA so //go:embed all:dist has real content.
COPY --from=frontend /app/backend/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /out/pi-dashboard .

# --- Stage 3: minimal runtime ---
FROM alpine:3.20
RUN adduser -D -H -u 10001 app && mkdir -p /data && chown app:app /data
COPY --from=backend /out/pi-dashboard /usr/local/bin/pi-dashboard
USER app
ENV PORT=8080 \
    DB_PATH=/data/data.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pi-dashboard"]
