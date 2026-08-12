.PHONY: test demo demo-legacy demo-http demo-subscriptions build

test:
	go test ./...

demo:
	go run ./cmd/mcp-host

demo-legacy:
	go run ./cmd/mcp-legacy-demo

demo-http:
	go run ./cmd/mcp-http-demo

demo-subscriptions:
	go run ./cmd/mcp-subscription-demo

build:
	mkdir -p bin
	go build -o bin/mcp-server ./cmd/mcp-server
	go build -o bin/mcp-host ./cmd/mcp-host
	go build -o bin/mcp-legacy-demo ./cmd/mcp-legacy-demo
	go build -o bin/mcp-http-demo ./cmd/mcp-http-demo
	go build -o bin/mcp-subscription-demo ./cmd/mcp-subscription-demo
