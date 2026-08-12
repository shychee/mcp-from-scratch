.PHONY: test demo demo-legacy demo-http demo-progress demo-subscriptions demo-task demo-oauth build

test:
	go test ./...

demo:
	go run ./cmd/mcp-host

demo-legacy:
	go run ./cmd/mcp-legacy-demo

demo-http:
	go run ./cmd/mcp-http-demo

demo-progress:
	go run ./cmd/mcp-progress-demo

demo-subscriptions:
	go run ./cmd/mcp-subscription-demo

demo-task:
	go run ./cmd/mcp-task-demo

demo-oauth:
	go run ./cmd/mcp-oauth-demo

build:
	mkdir -p bin
	go build -o bin/mcp-server ./cmd/mcp-server
	go build -o bin/mcp-host ./cmd/mcp-host
	go build -o bin/mcp-legacy-demo ./cmd/mcp-legacy-demo
	go build -o bin/mcp-http-demo ./cmd/mcp-http-demo
	go build -o bin/mcp-progress-demo ./cmd/mcp-progress-demo
	go build -o bin/mcp-subscription-demo ./cmd/mcp-subscription-demo
	go build -o bin/mcp-task-demo ./cmd/mcp-task-demo
	go build -o bin/mcp-oauth-demo ./cmd/mcp-oauth-demo
