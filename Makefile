BINARY_NAME := db-mcp
BIN_DIR := bin
CMD_PKG := ./cmd/db-mcp

.PHONY: all build test test-race vet fmt lint inspect clean

all: build

build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PKG)

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

lint:
	@test -z "$$(gofmt -l .)" || (echo "Unformatted files:" && gofmt -l . && exit 1)
	go vet ./...

inspect: build
	npx @modelcontextprotocol/inspector ./$(BIN_DIR)/$(BINARY_NAME) --database-url "postgres://localhost:5432/postgres"

clean:
	rm -rf $(BIN_DIR)
