.PHONY: format fmt format-check lint test build ci release clean

BINARY_NAME ?= surfarea
CMD_PATH ?= ./cmd/surfarea
BIN_DIR ?= bin
DIST_DIR ?= dist
VERSION ?= dev
HOST_GOOS := $(shell go env GOOS)
HOST_GOARCH := $(shell go env GOARCH)
PLATFORMS ?= $(HOST_GOOS)/$(HOST_GOARCH)

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
		GOOS=$$GOOS GOARCH=$$GOARCH go build -o "$$output_dir/$(BINARY_NAME)$$ext" $(CMD_PATH); \
		if [ "$$GOOS" = "windows" ]; then \
			(cd "$(DIST_DIR)" && zip -qr "$$name.zip" "$$name"); \
		else \
			tar -czf "$(DIST_DIR)/$$name.tar.gz" -C "$(DIST_DIR)" "$$name"; \
		fi; \
		rm -rf "$$output_dir"; \
	done

clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)
