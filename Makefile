.PHONY: build run test test-cover vet clean

# Build the TUI binary
build:
	go build -ldflags="-s -w" -o bin/openvibely-terminal ./cmd/openvibely-terminal

# Run against a local server (default http://localhost:3001)
run:
	go run ./cmd/openvibely-terminal

# Run tests
test:
	go test ./...

# Run tests with coverage
test-cover:
	go test -cover ./...

vet:
	go vet ./...

clean:
	rm -rf bin
