.PHONY: all build test lint bench run-autoconfig run-http run-batch help clean

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOMOD=$(GOCMD) mod
GOLINT=golangci-lint

# Main targets
all: lint build test

help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  build         Build the library"
	@echo "  test          Run unit tests with race detector and coverage"
	@echo "  lint          Run golangci-lint"
	@echo "  bench         Run all benchmarks"
	@echo "  run-autoconfig Run the auto-configuration example"
	@echo "  run-http      Run the HTTP server example"
	@echo "  run-batch     Run the batch processor example"
	@echo "  clean         Clean build artifacts and coverage files"
	@echo "  help          Show this help message"

build:
	@echo "Building adaptivepool..."
	$(GOBUILD) ./...

test:
	@echo "Running tests with race detector and coverage..."
	$(GOTEST) -race -v -coverprofile=coverage.out ./...
	@echo "To view coverage report: go tool cover -html=coverage.out"

lint:
	@echo "Running lint check..."
	$(GOLINT) run ./...

bench:
	@echo "Running benchmarks..."
	$(GOTEST) -bench=. -benchmem ./...

run-autoconfig:
	@echo "Running auto-configuration example..."
	$(GOCMD) run examples/autoconfig/main.go

run-http:
	@echo "Running HTTP server example..."
	$(GOCMD) run examples/http_server/main.go

run-batch:
	@echo "Running batch processor example..."
	$(GOCMD) run examples/batch_processor/main.go

run-1m-with:
	@echo "Running 1M RPS simulator WITH adaptivepool..."
	$(GOCMD) run examples/one_million_simulator/with_pool/main.go

run-1m-without:
	@echo "Running 1M RPS simulator WITHOUT adaptivepool (NAIVE)..."
	$(GOCMD) run examples/one_million_simulator/naive/main.go

run-comparison:
	@echo "Comparing Naive vs Adaptive Pool..."
	$(GOCMD) run examples/comparison/main.go

clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -f coverage.out

