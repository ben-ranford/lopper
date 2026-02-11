.PHONY: format fmt format-check lint test build ci release clean toolchain-check toolchain-install-macos

BINARY_NAME ?= surfarea
CMD_PATH ?= ./cmd/surfarea
BIN_DIR ?= bin
DIST_DIR ?= dist
VERSION ?= dev
HOST_GOOS := $(shell go env GOOS)
HOST_GOARCH := $(shell go env GOARCH)
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
	@command -v golangci-lint >/dev/null 2>&1 || (echo "golangci-lint not found in PATH"; exit 1)
	golangci-lint run ./...

test:
	go test ./...

build:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

ci: format-check lint test build

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
			GOOS=$$GOOS GOARCH=$$GOARCH go build -o "$$output_dir/$(BINARY_NAME)$$ext" $(CMD_PATH); \
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
			CC="$(ZIG) cc -target $$target" CXX="$(ZIG) c++ -target $$target" CGO_ENABLED=1 GOOS=$$GOOS GOARCH=$$GOARCH go build -o "$$output_dir/$(BINARY_NAME)$$ext" $(CMD_PATH); \
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
