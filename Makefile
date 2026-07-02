.PHONY: test demo build

test:
	go test ./...

demo:
	go run ./cmd/mcp-host

build:
	mkdir -p bin
	go build -o bin/mcp-server ./cmd/mcp-server
	go build -o bin/mcp-host ./cmd/mcp-host
