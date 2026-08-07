.PHONY: frontend build run test vet mcp up down clean tidy

# Build the React frontend into the Go embed directory (web/dist).
frontend:
	cd frontend && npm ci && npm run build

# Build the single self-contained backend binary (frontend embedded).
build:
	go build -o bin/yuno ./cmd/yuno

# Build the custom MCP servers (Factory create_agent + memory).
mcp:
	go build -o bin/yuno-create-agent ./cmd/mcp-create-agent
	go build -o bin/yuno-memory ./cmd/mcp-memory

# Run the backend locally (expects Postgres on DATABASE_URL).
run:
	go run ./cmd/yuno

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

# One-command local platform.
up:
	docker compose up --build

down:
	docker compose down

clean:
	rm -rf bin
