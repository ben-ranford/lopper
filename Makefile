.PHONY: format fmt format-check lint security test cov build ci release clean toolchain-check toolchain-install-macos hooks-install hooks-uninstall

BINARY_NAME ?= lopper
CMD_PATH ?= ./cmd/lopper
BIN_DIR ?= bin
DIST_DIR ?= dist
VERSION ?= dev
COVERAGE_FILE ?= .artifacts/coverage.out
COVERAGE_MIN ?= 90
GO ?= go
GO_TOOLCHAIN ?= go1.26.0
GO_CMD := GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO)
GOLANGCI_LINT_VERSION ?= v2.9.0
GOSEC_VERSION ?= v2.22.11
HOST_GOOS := $(shell $(GO_CMD) env GOOS)
HOST_GOARCH := $(shell $(GO_CMD) env GOARCH)
PLATFORMS ?= $(HOST_GOOS)/$(HOST_GOARCH)
ZIG ?= zig

format:
	gofmt -w .

fmt: format

format-check:
	@files=$$(gofmt -l .); \
	if [ -n "$$files" ]; then \
		echo "The following files are not gofmt-formatted:"; \
		echo "$$files"; \
		exit 1; \
	fi

lint:
	$(GO_CMD) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

security:
	$(GO_CMD) run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) ./...

test:
	$(GO_CMD) test ./...

cov:
	@mkdir -p $$(dirname "$(COVERAGE_FILE)")
	$(GO_CMD) test ./... -covermode=atomic -coverprofile="$(COVERAGE_FILE)"
	@total=$$($(GO_CMD) tool cover -func="$(COVERAGE_FILE)" | awk '/^total:/ {gsub("%","",$$3); print $$3}'); \
	echo "Total coverage: $$total% (required: >= $(COVERAGE_MIN)%)"; \
	printf "%s\n" "$$total" > .artifacts/coverage-total.txt; \
	awk "BEGIN { exit !($$total >= $(COVERAGE_MIN)) }" || (echo "Coverage gate failed: $$total% < $(COVERAGE_MIN)%"; exit 1)

build:
	mkdir -p $(BIN_DIR)
	$(GO_CMD) build -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

ci: format-check lint security test build

toolchain-check:
	@command -v go >/dev/null 2>&1 || (echo "go not found in PATH"; exit 1)
	@command -v $(ZIG) >/dev/null 2>&1 || (echo "zig not found in PATH (required for cross-CGO builds)"; exit 1)

toolchain-install-macos:
	@command -v brew >/dev/null 2>&1 || (echo "homebrew not found"; exit 1)
	brew install zig

release:
	rm -rf $(DIST_DIR)
	mkdir -p $(DIST_DIR)
	@set -e; for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*}; \
		GOARCH=$${platform#*/}; \
		name="$(BINARY_NAME)_$(VERSION)_$${GOOS}_$${GOARCH}"; \
		output_dir="$(DIST_DIR)/$$name"; \
		mkdir -p "$$output_dir"; \
		ext=""; \
		if [ "$$GOOS" = "windows" ]; then ext=".exe"; fi; \
		echo "Building $$name"; \
		if [ "$$GOOS" = "$(HOST_GOOS)" ] && [ "$$GOARCH" = "$(HOST_GOARCH)" ]; then \
			GOOS=$$GOOS GOARCH=$$GOARCH $(GO_CMD) build -o "$$output_dir/$(BINARY_NAME)$$ext" $(CMD_PATH); \
		else \
			if [ "$$GOOS" = "darwin" ]; then \
				echo "Cross-compiling to $$GOOS/$$GOARCH is not supported in this setup."; \
				echo "Build darwin targets on a matching macOS runner (native arch)."; \
				exit 1; \
			fi; \
			command -v $(ZIG) >/dev/null 2>&1 || (echo "zig not found in PATH (required for cross compile $$platform)"; exit 1); \
			case "$$GOOS/$$GOARCH" in \
				linux/amd64) target="x86_64-linux-gnu" ;; \
				linux/arm64) target="aarch64-linux-gnu" ;; \
				windows/amd64) target="x86_64-windows-gnu" ;; \
				windows/arm64) target="aarch64-windows-gnu" ;; \
				*) echo "Unsupported cross target $$GOOS/$$GOARCH"; exit 1 ;; \
			esac; \
			CC="$(ZIG) cc -target $$target" CXX="$(ZIG) c++ -target $$target" CGO_ENABLED=1 GOOS=$$GOOS GOARCH=$$GOARCH $(GO_CMD) build -o "$$output_dir/$(BINARY_NAME)$$ext" $(CMD_PATH); \
		fi; \
		if [ "$$GOOS" = "windows" ]; then \
			(cd "$(DIST_DIR)" && zip -qr "$$name.zip" "$$name"); \
		else \
			tar -czf "$(DIST_DIR)/$$name.tar.gz" -C "$(DIST_DIR)" "$$name"; \
		fi; \
		rm -rf "$$output_dir"; \
	done

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)

hooks-install:
	@git config core.hooksPath .githooks
	@chmod +x .githooks/pre-commit
	@echo "Installed git hooks from .githooks"

hooks-uninstall:
	@git config --unset core.hooksPath || true
	@echo "Removed custom core.hooksPath hook configuration"
