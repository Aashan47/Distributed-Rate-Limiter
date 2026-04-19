GO      ?= go
BUF     ?= buf
BIN_DIR ?= bin

MODULE := github.com/Aashan47/Distributed-Rate-Limiter

.PHONY: all
all: tidy generate build test

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: generate
generate:
	$(BUF) dep update
	$(BUF) generate

.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/server ./cmd/server

.PHONY: run
run: build
	./$(BIN_DIR)/server

.PHONY: test
test:
	$(GO) test -race -count=1 ./...

.PHONY: cover
cover:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

.PHONY: lint
lint:
	$(GO) vet ./...
	$(BUF) lint

.PHONY: docker-build
docker-build:
	docker build -f deployments/docker/Dockerfile -t rate-limiter:dev .

.PHONY: up
up:
	docker compose -f deployments/docker-compose.yml up --build

.PHONY: down
down:
	docker compose -f deployments/docker-compose.yml down -v

.PHONY: clean
clean:
	rm -rf $(BIN_DIR) gen coverage.* *.out
