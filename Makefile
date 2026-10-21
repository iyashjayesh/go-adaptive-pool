.PHONY: all build test lint bench run-http run-batch run-observability help clean

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOMOD=$(GOCMD) mod
GOLINT=golangci-lint

# The recorder adapters and the observability example are separate modules,
# which ./... does not enter, so each target runs in every module.
MODULES=. promrecorder otelrecorder examples/observability
TESTED_MODULES=. promrecorder otelrecorder
define each
	@for d in $(1); do echo "== $$d"; (cd $$d && $(2)) || exit 1; done
endef

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
	@echo "  run-http      Run the HTTP server example"
	@echo "  run-batch     Run the batch processor example"
	@echo "  run-observability Run the Prometheus/Grafana observability example"
	@echo "  clean         Clean build artifacts and coverage files"
	@echo "  help          Show this help message"

build:
	@echo "Building adaptivepool..."
	$(call each,$(MODULES),$(GOBUILD) -o /dev/null ./...)

test:
	@echo "Running tests with race detector and coverage..."
	$(call each,$(TESTED_MODULES),$(GOTEST) -race -v -coverprofile=coverage.out ./...)
	@echo "To view a coverage report: cd <module> && go tool cover -html=coverage.out"

lint:
	@echo "Running lint check..."
	$(call each,$(MODULES),$(GOLINT) run ./...)

bench:
	@echo "Running benchmarks..."
	$(call each,$(TESTED_MODULES),$(GOTEST) -bench=. -benchmem ./...)

run-http:
	@echo "Running HTTP server example..."
	$(GOCMD) run examples/http_server/main.go

run-batch:
	@echo "Running batch processor example..."
	$(GOCMD) run examples/batch_processor/main.go

run-observability:
	@echo "Running observability example (metrics on :2112/metrics unless ARGS sets -metrics-addr)..."
	$(GOCMD) run -C examples/observability . $(ARGS)

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

