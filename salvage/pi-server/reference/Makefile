.PHONY: dev frontend build run cross test clean

# Build the Vue frontend into the Go embed directory.
frontend:
	cd frontend && npm ci && npm run build

# Build the single self-contained binary (frontend embedded) for the host arch.
build: frontend
	cd backend && go build -o ../bin/pi-dashboard .

# Run the backend locally (expects a prior `make frontend` for the embedded UI).
run:
	cd backend && go run .

# Cross-compile a CGO-free binary for a 64-bit Raspberry Pi (Pi 3/4/5, 64-bit OS).
# For a 32-bit OS use: GOARCH=arm GOARM=7
cross: frontend
	cd backend && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o ../bin/pi-dashboard-arm64 .

test:
	cd backend && go vet ./... && go test ./...

clean:
	rm -rf bin backend/web/dist/assets backend/web/dist/index.html
