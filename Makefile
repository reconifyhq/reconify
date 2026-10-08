.PHONY: help build build-all test lint fmt-check mod-check dep-check security check preflight clean install eval-release eval-smoke eval-summary eval-compare check-fast bench-smoke bench-deterministic bench-realistic bench-adversarial-smoke bench-adversarial bench-adversarial-cold bench-full

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
LDFLAGS := -ldflags "-X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)"

help: ## Show this help message
	@echo 'Reconify CLI - Available Commands:'
	@echo ''
	@echo 'Build:'
	@echo '  make build      - Build CLI binary'
	@echo '  make build-all  - Cross-compile for all platforms'
	@echo '  make install    - Install CLI to $$GOPATH/bin'
	@echo ''
	@echo 'Testing:'
	@echo '  make test       - Run all tests with race detection and coverage'
	@echo '  make fmt-check  - Fail if Go files need gofmt'
	@echo '  make mod-check  - Verify modules and fail on go.mod/go.sum drift'
	@echo '  make dep-check  - Enforce acyclic engine package boundaries'
	@echo '  make lint       - Run linters'
	@echo '  make security   - Run govulncheck and gosec'
	@echo '  make check      - Run the local equivalent of GitHub Actions checks'
	@echo '  make check-fast - Inner-loop check: gofmt, go vet, go test (NOT a replacement for make check)'
	@echo '  make preflight  - Alias for make check'
	@echo '  make bench-smoke                - Run small correctness benchmarks (includes adversarial)'
	@echo '  make bench-deterministic        - Run deterministic 1-N benchmarks'
	@echo '  make bench-realistic            - Run realistic synthetic benchmarks'
	@echo '  make bench-adversarial-smoke    - Run adversarial semantic smoke matrix'
	@echo '  make bench-adversarial          - Run adversarial scale benchmark (100k rows)'
	@echo '  make bench-adversarial-cold     - Run adversarial cold-cache measurement (local/manual)'
	@echo '  make bench-full                 - Run larger benchmark suite (includes adversarial)'
	@echo '  make eval-release BASELINE_VERSION=x.y.z - Run the local skill release gate'
	@echo '  make eval-smoke                 - Run one agent, one trial, core+messy scenarios (EVAL_AGENT, EVAL_MODEL)'
	@echo '  make eval-summary               - Summarize the last eval-smoke report as markdown'
	@echo '  make eval-compare BASE=a.json HEAD=b.json - Compare two eval reports (exit 3 on regression)'
	@echo ''
	@echo 'Clean:'
	@echo '  make clean      - Clean build artifacts'

# Build
build: ## Build CLI binary
	go build $(LDFLAGS) -o reconify ./cmd/reconify

build-all: build ## Cross-compile CLI for all platforms
	@echo "Building CLI for multiple platforms..."
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/reconify-linux-amd64 ./cmd/reconify
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o dist/reconify-darwin-amd64 ./cmd/reconify
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/reconify-darwin-arm64 ./cmd/reconify
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/reconify-windows-amd64.exe ./cmd/reconify

# Testing
test: ## Run all tests
	go test -v -race -coverprofile=coverage.out ./...

fmt-check: ## Fail if Go files need gofmt
	@test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))" || \
		(echo "Go files need gofmt:"; gofmt -l $$(find . -name '*.go' -not -path './vendor/*'); exit 1)

mod-check: ## Verify modules and fail on go.mod/go.sum drift
	go mod tidy -diff
	go mod verify

dep-check: ## Enforce acyclic engine package boundaries
	@for pkg in domain telemetry parser index matching output reconcile; do \
		if go list -deps "github.com/reconifyhq/reconify/engine/$$pkg" | grep -qE 'github.com/reconifyhq/reconify/(engine$$|internal/cli)'; then \
			echo "FAIL: engine/$$pkg imports engine or internal/cli"; exit 1; \
		fi; \
	done

# Linting
lint: ## Run linters
	@if command -v golangci-lint > /dev/null; then \
		GOOS=linux GOARCH=amd64 golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 1; \
	fi

security: ## Run vulnerability and security checks
	@if command -v govulncheck > /dev/null; then \
		GOOS=linux GOARCH=amd64 govulncheck ./...; \
	elif test -x "$$(go env GOPATH)/bin/govulncheck"; then \
		GOOS=linux GOARCH=amd64 "$$(go env GOPATH)/bin/govulncheck" ./...; \
	else \
		echo "govulncheck not installed. Install with: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
		exit 1; \
	fi
	@if command -v golangci-lint > /dev/null; then \
		GOOS=linux GOARCH=amd64 golangci-lint run --enable-only=gosec ./...; \
	else \
		echo "golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 1; \
	fi

check: mod-check fmt-check dep-check lint security test build bench-smoke ## Run the local equivalent of GitHub Actions checks

eval-release: ## Run the opt-in candidate/released/no-skill evaluation matrix
	@test -n "$(BASELINE_VERSION)" || (echo 'BASELINE_VERSION is required'; exit 2)
	go run ./cmd/reconify-eval release --baseline-version "$(BASELINE_VERSION)" --model claude=$${CLAUDE_MODEL:?set CLAUDE_MODEL} --model codex=$${CODEX_MODEL:?set CODEX_MODEL} --model gemini=$${GEMINI_MODEL:?set GEMINI_MODEL} --model opencode=$${OPENCODE_MODEL:?set OPENCODE_MODEL}

check-fast: fmt-check ## Inner-loop check (gofmt, go vet, go test); run make check before opening a PR
	go vet ./...
	go test ./...

EVAL_SMOKE_DIR := .context/evals/smoke

eval-smoke: ## Run one agent, one trial, core+messy scenarios (EVAL_AGENT=claude, optional EVAL_MODEL)
	rm -rf $(EVAL_SMOKE_DIR)
	@mkdir -p $(EVAL_SMOKE_DIR) .context/evals/bin
	go build -o .context/evals/bin/reconify ./cmd/reconify
	agent=$${EVAL_AGENT:-claude}; \
	go run ./cmd/reconify-eval run --reconify .context/evals/bin/reconify --agent "$$agent" --trials 1 --tag core --tag messy \
		--artifacts $(EVAL_SMOKE_DIR)/artifacts --out $(EVAL_SMOKE_DIR)/report.json \
		$${EVAL_MODEL:+--model "$$agent=$$EVAL_MODEL"}

eval-summary: ## Summarize the last eval-smoke report as markdown
	go run ./cmd/reconify-eval summarize $(EVAL_SMOKE_DIR)/report.json --markdown

eval-compare: ## Compare two eval reports: make eval-compare BASE=base.json HEAD=head.json (exit 3 on regression)
	@test -n "$(BASE)" -a -n "$(HEAD)" || (echo 'BASE and HEAD are required'; exit 2)
	go run ./cmd/reconify-eval compare "$(BASE)" "$(HEAD)" --markdown

preflight: check ## Alias for make check

# Benchmarks
bench-smoke: ## Run small correctness benchmarks (deterministic + realistic + adversarial)
	./benchmarks/deterministic/run.sh --rows 1000
	./benchmarks/realistic/run.sh --rows 1000
	./benchmarks/adversarial/run.sh --smoke

bench-deterministic: ## Run deterministic 1-N benchmarks
	./benchmarks/deterministic/run.sh --rows 100000

bench-realistic: ## Run realistic synthetic benchmarks
	./benchmarks/realistic/run.sh --rows 100000

bench-adversarial-smoke: ## Run adversarial semantic smoke matrix (500 rows)
	./benchmarks/adversarial/run.sh --smoke

bench-adversarial: ## Run adversarial scale benchmark (100k rows, warm cache)
	./benchmarks/adversarial/run.sh --rows 100000

bench-adversarial-cold: ## Run adversarial cold-cache measurement (local/manual only)
	./benchmarks/adversarial/run.sh --rows 100000 --cache-mode cold

bench-full: ## Run larger benchmark suite (1M rows deterministic + realistic + 100k adversarial)
	./benchmarks/deterministic/run.sh --rows 1000000
	./benchmarks/realistic/run.sh --rows 1000000
	./benchmarks/adversarial/run.sh --rows 100000

# Installation
install: ## Install CLI to GOPATH/bin
	go install $(LDFLAGS) ./cmd/reconify

# Clean
clean: ## Clean build artifacts
	rm -f reconify coverage.out
	rm -rf dist/
	rm -rf benchmarks/.out/

.DEFAULT_GOAL := help
