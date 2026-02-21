.PHONY: format fmt format-check lint dup-check security test cov build ci demos demos-check release clean toolchain-check toolchain-install toolchain-install-macos toolchain-install-linux tools-install setup hooks-install hooks-uninstall

BINARY_NAME ?= lopper
CMD_PATH ?= ./cmd/lopper
BIN_DIR ?= bin
DIST_DIR ?= dist
VERSION ?= dev
COVERAGE_FILE ?= .artifacts/coverage.out
COVERAGE_MIN ?= 95
GO ?= go
GO_TOOLCHAIN ?= go1.26.0
GO_CMD := GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO)
GOLANGCI_LINT_VERSION ?= v2.9.0
GOSEC_VERSION ?= v2.22.11
DUPL_VERSION ?= f008fcf5e62793d38bda510ee37aab8b0c68e76c
DUPLICATION_MAX ?= 3
DUPLICATION_TOKEN_THRESHOLD ?= 55
DUPLICATION_BASE ?= origin/main
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

dup-check:
	@requested_base_ref="$(DUPLICATION_BASE)"; \
	base_ref="$$requested_base_ref"; \
	used_fallback=0; \
	if ! git rev-parse --verify -q "$$base_ref^{commit}" >/dev/null; then \
		echo "Warning: duplication base ref '$$base_ref' not found; falling back to 'HEAD~1'. This may miss duplication introduced earlier in this branch."; \
		base_ref="HEAD~1"; \
		used_fallback=1; \
	fi; \
	if ! git rev-parse --verify -q "$$base_ref^{commit}" >/dev/null; then \
		echo "No valid duplication base ref found; skipping new-code duplication check."; \
		exit 0; \
	fi; \
	if ! base_commit=$$(git merge-base "$$base_ref" HEAD 2>/dev/null); then \
		echo "Base ref '$$base_ref' is not related to HEAD; skipping new-code duplication check."; \
		exit 0; \
	fi; \
	added_file=$$(mktemp); \
	dup_file=$$(mktemp); \
	trap 'rm -f "$$added_file" "$$dup_file"' EXIT INT TERM; \
	git diff --unified=0 --no-color "$$base_commit"..HEAD -- '*.go' | \
	awk '/^\+\+\+ b\// { file = substr($$0, 7); next } $$1 == "@@" { line = $$3; sub(/^\+/, "", line); split(line, parts, ","); start = parts[1] + 0; count = (parts[2] == "" ? 1 : parts[2] + 0); for (i = 0; i < count; i++) if (file != "") print file ":" (start + i) }' | sort -u > "$$added_file"; \
	added=$$(wc -l < "$$added_file" | tr -d ' '); \
	if [ "$$added" -eq 0 ]; then \
		if [ "$$used_fallback" -eq 1 ]; then \
			echo "New-code duplication: 0.00% (no changed Go lines vs fallback base $$base_ref; requested $$requested_base_ref)"; \
		else \
			echo "New-code duplication: 0.00% (no changed Go lines vs $$base_ref)"; \
		fi; \
		exit 0; \
	fi; \
	$(GO_CMD) run github.com/mibk/dupl@$(DUPL_VERSION) -t $(DUPLICATION_TOKEN_THRESHOLD) -plumbing . | \
	awk -F: '{ n = split($$2, r, "-"); if (n != 2 || r[1] == "" || r[2] == "") next; if (r[1] !~ /^[0-9]+$$/ || r[2] !~ /^[0-9]+$$/) next; start = r[1] + 0; end = r[2] + 0; if (start > end) next; for (i = start; i <= end; i++) print $$1 ":" i }' | sort -u > "$$dup_file"; \
	dup_added=$$(comm -12 "$$added_file" "$$dup_file" | wc -l | tr -d ' '); \
	pct=$$(awk -v d="$$dup_added" -v t="$$added" 'BEGIN { d += 0; t += 0; printf "%.2f", (d / t) * 100 }'); \
	if [ "$$used_fallback" -eq 1 ]; then \
		base_msg="fallback $$base_ref (requested $$requested_base_ref)"; \
	else \
		base_msg="$$base_ref"; \
	fi; \
	echo "New-code duplication: $$pct% (duplicated added lines: $$dup_added / $$added, max: $(DUPLICATION_MAX)%, threshold: $(DUPLICATION_TOKEN_THRESHOLD) tokens, base: $$base_msg)"; \
	awk -v p="$$pct" 'BEGIN { exit !(p <= $(DUPLICATION_MAX)) }' || (echo "Duplication gate failed: $$pct% > $(DUPLICATION_MAX)%"; exit 1)

security:
	$(GO_CMD) run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) ./...

test:
	$(GO_CMD) test ./...

cov:
	@mkdir -p $$(dirname "$(COVERAGE_FILE)")
	@pkgs=$$($(GO_CMD) list ./... | grep -v '/internal/testutil$$'); \
	$(GO_CMD) test $$pkgs -covermode=atomic -coverprofile="$(COVERAGE_FILE)"
	@total=$$($(GO_CMD) tool cover -func="$(COVERAGE_FILE)" | awk '/^total:/ {gsub("%","",$$3); print $$3}'); \
	echo "Total coverage: $$total% (required: >= $(COVERAGE_MIN)%)"; \
	printf "%s\n" "$$total" > .artifacts/coverage-total.txt; \
	awk "BEGIN { exit !($$total >= $(COVERAGE_MIN)) }" || (echo "Coverage gate failed: $$total% < $(COVERAGE_MIN)%"; exit 1)

build:
	mkdir -p $(BIN_DIR)
	$(GO_CMD) build -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

ci: format-check lint dup-check security test build

demos:
	./scripts/demos/render.sh

demos-check:
	./scripts/demos/check.sh

toolchain-check:
	@command -v go >/dev/null 2>&1 || (echo "go not found in PATH"; exit 1)
	@version="$$(go env GOVERSION 2>/dev/null || go version | awk '{print $$3}')"; \
	version="$${version#go}"; \
	major="$${version%%.*}"; \
	rest="$${version#*.}"; \
	minor="$${rest%%.*}"; \
	major="$${major%%[^0-9]*}"; \
	minor="$${minor%%[^0-9]*}"; \
	if [ -z "$$major" ] || [ -z "$$minor" ]; then \
		echo "Unable to parse Go version: $$version"; \
		exit 1; \
	fi; \
	if [ "$$major" -lt 1 ] || { [ "$$major" -eq 1 ] && [ "$$minor" -lt 26 ]; }; then \
		echo "Go 1.26.x or newer is required (found $$version)."; \
		echo "Install/update Go from https://go.dev/dl/ or use your package manager's newest Go release."; \
		exit 1; \
	fi
	@command -v $(ZIG) >/dev/null 2>&1 || (echo "zig not found in PATH (required for cross-CGO builds)"; exit 1)

toolchain-install:
	@uname_s="$$(uname -s)"; \
	case "$$uname_s" in \
		Darwin) $(MAKE) toolchain-install-macos ;; \
		Linux) $(MAKE) toolchain-install-linux ;; \
		*) echo "Unsupported OS: $$uname_s"; exit 1 ;; \
	esac

toolchain-install-macos:
	@command -v brew >/dev/null 2>&1 || (echo "homebrew not found"; exit 1)
	brew install go zig

toolchain-install-linux:
	@if command -v apt-get >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO apt-get update; \
		$$SUDO apt-get install -y golang-go zig; \
	elif command -v dnf >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO dnf install -y golang zig; \
	elif command -v pacman >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO pacman -Syu --noconfirm --needed go zig; \
	else \
		echo "No supported package manager found (need apt-get, dnf, or pacman)"; \
		exit 1; \
	fi

tools-install:
	$(GO_CMD) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO_CMD) install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)

setup: toolchain-install
	$(GO_CMD) mod download
	$(MAKE) toolchain-check
	@echo "Toolchain ready. Use: make test && make build"

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
