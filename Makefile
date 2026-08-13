.PHONY: test interop smoke demo demo-legacy demo-http demo-progress demo-subscriptions demo-task demo-oauth demo-model build

test:
	go test ./...

interop:
	go test ./internal/interop -count=1

smoke:
	go test ./... -count=1
	go vet ./...
	$(MAKE) build
	$(MAKE) demo
	$(MAKE) demo-legacy
	$(MAKE) demo-http
	$(MAKE) demo-progress
	$(MAKE) demo-subscriptions
	$(MAKE) demo-task
	$(MAKE) demo-oauth
	$(MAKE) demo-model
	$(MAKE) interop

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

demo-model:
	go run ./cmd/mcp-model-demo

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
	go build -o bin/mcp-model-demo ./cmd/mcp-model-demo
	go build -o bin/mcp-official-fixture ./cmd/mcp-official-fixture
