.PHONY: format fmt format-check gostyle lint actionlint shellcheck mod-check dup-check suppression-check security vuln-check test test-leaks test-race bench-mem bench-delta bench-gate cov build ci smoke demos demos-check mem-profiles release clean toolchain-check toolchain-install toolchain-install-macos toolchain-install-linux tools-install setup hooks-install hooks-uninstall sync-version vscode-extension-install vscode-extension-compile vscode-extension-test vscode-extension-package

BINARY_NAME ?= lopper
CMD_PATH ?= ./cmd/lopper
BIN_DIR ?= bin
DIST_DIR ?= dist
VSCODE_EXTENSION_DIR ?= extensions/vscode-lopper
VSCODE_EXTENSION_PACKAGE_PATH ?= $(DIST_DIR)/vscode-lopper.vsix
VERSION ?= dev
VERSION_PKG ?= github.com/ben-ranford/lopper/internal/version
GIT_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COVERAGE_FILE ?= .artifacts/coverage.out
COVERAGE_MIN ?= 98
GO ?= go
GO_TOOLCHAIN ?= go1.26.1
GO_CMD := GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO)
GOLANGCI_LINT_VERSION ?= v2.9.0
GOSTYLE_VERSION ?= v0.25.3
GOSEC_VERSION ?= v2.22.11
ACTIONLINT_VERSION ?= v1.7.11
GOVULNCHECK_VERSION ?= v1.1.4
DUPL_VERSION ?= f008fcf5e62793d38bda510ee37aab8b0c68e76c
DUPLICATION_MAX ?= 3
DUPLICATION_TOKEN_THRESHOLD ?= 55
DUPLICATION_BASE ?= origin/main
SUPPRESSION_BASE ?= origin/main
BENCH_COUNT ?= 3
BENCH_TIME ?= 200ms
MEMORY_BENCH_PACKAGES ?= ./internal/lang/shared ./internal/report
MEMORY_BENCH_BASE ?= origin/main
MEMORY_BENCH_MAX_BYTES_PCT ?= 15
MEMORY_BENCH_MAX_ALLOCS_PCT ?= 10
BENCH_OUTPUT ?= .artifacts/bench.out
BENCH_BASE_OUTPUT ?= .artifacts/bench-base.out
BENCH_HEAD_OUTPUT ?= .artifacts/bench-head.out
MEMORY_BENCH_SUMMARY ?= .artifacts/memory-bench-summary.md
MEMORY_BENCH_STATUS ?= .artifacts/memory-bench-status.txt
MEMORY_BENCH_ENFORCE ?= 1
MEM_PROFILE_DIR ?= .artifacts/memory-profiles
MEM_PROFILE_PACKAGES ?= ./internal/lang/dotnet ./internal/lang/rust ./internal/analysis ./internal/lang/golang
MEM_PROFILE_COUNT ?= 1
MEM_PROFILE_NODECOUNT ?= 20
HOST_GOOS := $(shell $(GO_CMD) env GOOS)
HOST_GOARCH := $(shell $(GO_CMD) env GOARCH)
PLATFORMS ?= $(HOST_GOOS)/$(HOST_GOARCH)
ZIG ?= zig
GO_VERSION_LDFLAGS = -X $(VERSION_PKG).version=$(VERSION) -X $(VERSION_PKG).commit=$(GIT_COMMIT) -X $(VERSION_PKG).buildDate=$(BUILD_DATE)
BUILD_GO_LDFLAGS ?= $(GO_VERSION_LDFLAGS)
RELEASE_GO_LDFLAGS ?= -s -w $(GO_VERSION_LDFLAGS)

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

gostyle:
	GOFLAGS=-buildvcs=false $(GO_CMD) run github.com/k1LoW/gostyle@$(GOSTYLE_VERSION) run -c .gostyle.yml ./...

lint:
	$(GO_CMD) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...
	$(MAKE) gostyle

actionlint:
	$(GO_CMD) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

shellcheck:
	@command -v shellcheck >/dev/null 2>&1 || (echo "shellcheck not found in PATH"; exit 1)
	@if [ -z "$$(find scripts .githooks -type f \( -name '*.sh' -o -path '.githooks/*' \) -print -quit)" ]; then \
		echo "No shell scripts found for shellcheck."; \
		exit 0; \
	fi; \
	find scripts .githooks -type f \( -name '*.sh' -o -path '.githooks/*' \) -print0 | xargs -0 shellcheck

mod-check:
	$(GO_CMD) mod tidy -diff
	$(GO_CMD) mod verify

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
	git diff --unified=0 --no-color "$$base_commit"..HEAD -- '*.go' ':(exclude)**/goleak_test.go' | \
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

suppression-check:
	SUPPRESSION_BASE="$(SUPPRESSION_BASE)" ./scripts/check-inline-suppressions.sh

security:
	$(GO_CMD) run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) ./...

vuln-check:
	$(GO_CMD) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

test:
	$(GO_CMD) test ./...

test-leaks:
	GOLEAK=1 $(GO_CMD) test ./...

test-race:
	$(GO_CMD) test -race ./...

bench-mem:
	@mkdir -p $$(dirname "$(BENCH_OUTPUT)"); \
	bench_output_tmp=$$(mktemp); \
	trap 'rm -f "$$bench_output_tmp"' EXIT INT TERM; \
	if ! GOFLAGS=-buildvcs=false $(GO_CMD) test -run '^$$' -bench . -benchmem -count=$(BENCH_COUNT) -benchtime=$(BENCH_TIME) $(MEMORY_BENCH_PACKAGES) > "$$bench_output_tmp" 2>&1; then \
		cat "$$bench_output_tmp"; \
		exit 1; \
	fi; \
	cat "$$bench_output_tmp"; \
	cp "$$bench_output_tmp" "$(BENCH_OUTPUT)"

bench-delta:
	$(GO_CMD) run ./tools/benchdelta -base "$(BENCH_BASE_OUTPUT)" -head "$(BENCH_HEAD_OUTPUT)" -max-bytes-pct "$(MEMORY_BENCH_MAX_BYTES_PCT)" -max-allocs-pct "$(MEMORY_BENCH_MAX_ALLOCS_PCT)" -summary-out "$(MEMORY_BENCH_SUMMARY)"

bench-gate:
	@set -eu; \
	requested_base_ref="$(MEMORY_BENCH_BASE)"; \
	base_ref="$$requested_base_ref"; \
	used_fallback=0; \
	if ! git rev-parse --verify -q "$$base_ref^{commit}" >/dev/null; then \
		echo "Warning: memory benchmark base ref '$$base_ref' not found; falling back to 'HEAD~1'."; \
		base_ref="HEAD~1"; \
		used_fallback=1; \
	fi; \
	mkdir -p $$(dirname "$(BENCH_BASE_OUTPUT)"); \
	if ! git rev-parse --verify -q "$$base_ref^{commit}" >/dev/null; then \
		echo "No valid memory benchmark base ref found; skipping memory benchmark gate."; \
		printf "## Memory Benchmarks\n\nNo valid base ref found; skipping memory benchmark gate.\n" > "$(MEMORY_BENCH_SUMMARY)"; \
		printf "0\n" > "$(MEMORY_BENCH_STATUS)"; \
		exit 0; \
	fi; \
	if ! base_commit=$$(git merge-base "$$base_ref" HEAD 2>/dev/null); then \
		echo "Base ref '$$base_ref' is not related to HEAD; skipping memory benchmark gate."; \
		printf "## Memory Benchmarks\n\nBase ref '%s' is not related to HEAD; skipping memory benchmark gate.\n" "$$base_ref" > "$(MEMORY_BENCH_SUMMARY)"; \
		printf "0\n" > "$(MEMORY_BENCH_STATUS)"; \
		exit 0; \
	fi; \
	bench_dir=$$(mktemp -d); \
	base_tree="$$bench_dir/base"; \
	base_output_tmp=$$(mktemp); \
	head_output_tmp=$$(mktemp); \
	cleanup() { git worktree remove --force "$$base_tree" >/dev/null 2>&1 || true; rm -rf "$$bench_dir"; rm -f "$$base_output_tmp" "$$head_output_tmp"; }; \
	trap cleanup EXIT INT TERM; \
	if [ "$$used_fallback" -eq 1 ]; then \
		echo "Running memory benchmark delta against fallback base $$base_ref (requested $$requested_base_ref)."; \
	else \
		echo "Running memory benchmark delta against $$base_ref."; \
	fi; \
	git worktree add --detach "$$base_tree" "$$base_commit" >/dev/null; \
	if ! (cd "$$base_tree" && GOFLAGS=-buildvcs=false $(GO_CMD) test -run '^$$' -bench . -benchmem -count=$(BENCH_COUNT) -benchtime=$(BENCH_TIME) $(MEMORY_BENCH_PACKAGES)) > "$$base_output_tmp" 2>&1; then \
		cat "$$base_output_tmp"; \
		exit 1; \
	fi; \
	cat "$$base_output_tmp"; \
	cp "$$base_output_tmp" "$(BENCH_BASE_OUTPUT)"; \
	if ! GOFLAGS=-buildvcs=false $(GO_CMD) test -run '^$$' -bench . -benchmem -count=$(BENCH_COUNT) -benchtime=$(BENCH_TIME) $(MEMORY_BENCH_PACKAGES) > "$$head_output_tmp" 2>&1; then \
		cat "$$head_output_tmp"; \
		exit 1; \
	fi; \
	cat "$$head_output_tmp"; \
	cp "$$head_output_tmp" "$(BENCH_HEAD_OUTPUT)"; \
	set +e; \
	$(GO_CMD) run ./tools/benchdelta -base "$(BENCH_BASE_OUTPUT)" -head "$(BENCH_HEAD_OUTPUT)" -max-bytes-pct "$(MEMORY_BENCH_MAX_BYTES_PCT)" -max-allocs-pct "$(MEMORY_BENCH_MAX_ALLOCS_PCT)" -summary-out "$(MEMORY_BENCH_SUMMARY)"; \
	status=$$?; \
	set -e; \
	printf "%s\n" "$$status" > "$(MEMORY_BENCH_STATUS)"; \
	if [ "$(MEMORY_BENCH_ENFORCE)" = "0" ]; then \
		if [ "$$status" -eq 1 ]; then \
			exit 0; \
		fi; \
	fi; \
	exit "$$status"

cov:
	@mkdir -p $$(dirname "$(COVERAGE_FILE)")
	@pkgs=$$(GOFLAGS=-buildvcs=false $(GO_CMD) list ./... | grep -Ev '/internal/testutil$$|/internal/testsupport$$|/tools/benchdelta$$'); \
	GOFLAGS=-buildvcs=false $(GO_CMD) test $$pkgs -covermode=atomic -coverprofile="$(COVERAGE_FILE)"
	@total=$$($(GO_CMD) tool cover -func="$(COVERAGE_FILE)" | awk '/^total:/ {gsub("%","",$$3); print $$3}'); \
	echo "Total coverage: $$total% (required: >= $(COVERAGE_MIN)%)"; \
	printf "%s\n" "$$total" > .artifacts/coverage-total.txt; \
	awk "BEGIN { exit !($$total >= $(COVERAGE_MIN)) }" || (echo "Coverage gate failed: $$total% < $(COVERAGE_MIN)%"; exit 1)

build:
	mkdir -p $(BIN_DIR)
	GOFLAGS=-buildvcs=false $(GO_CMD) build -ldflags "$(BUILD_GO_LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

ci: format-check mod-check lint actionlint shellcheck dup-check suppression-check security vuln-check test test-leaks test-race bench-gate build cov

smoke: mod-check test-race build

demos:
	./scripts/demos/render.sh

demos-check:
	./scripts/demos/check.sh

mem-profiles:
	MEM_PROFILE_STAMP="$(MEM_PROFILE_STAMP)" \
	MEM_PROFILE_DIR="$(MEM_PROFILE_DIR)" \
	MEM_PROFILE_PACKAGES="$(MEM_PROFILE_PACKAGES)" \
	MEM_PROFILE_COUNT="$(MEM_PROFILE_COUNT)" \
	MEM_PROFILE_NODECOUNT="$(MEM_PROFILE_NODECOUNT)" \
	GO="$(GO)" \
	GOTOOLCHAIN="$(GO_TOOLCHAIN)" \
	./scripts/profiling/memory_profiles.sh

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
	@command -v shellcheck >/dev/null 2>&1 || (echo "shellcheck not found in PATH (required for shell script CI checks)"; exit 1)

toolchain-install:
	@uname_s="$$(uname -s)"; \
	case "$$uname_s" in \
		Darwin) $(MAKE) toolchain-install-macos ;; \
		Linux) $(MAKE) toolchain-install-linux ;; \
		*) echo "Unsupported OS: $$uname_s"; exit 1 ;; \
	esac

toolchain-install-macos:
	@command -v brew >/dev/null 2>&1 || (echo "homebrew not found"; exit 1)
	brew install go zig shellcheck

toolchain-install-linux:
	@if command -v apt-get >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO apt-get update; \
		$$SUDO apt-get install -y golang-go zig shellcheck; \
	elif command -v dnf >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO dnf install -y golang zig ShellCheck; \
	elif command -v pacman >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO pacman -Syu --noconfirm --needed go zig shellcheck; \
	else \
		echo "No supported package manager found (need apt-get, dnf, or pacman)"; \
		exit 1; \
	fi

tools-install:
	$(GO_CMD) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO_CMD) install github.com/k1LoW/gostyle@$(GOSTYLE_VERSION)
	$(GO_CMD) install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	$(GO_CMD) install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	$(GO_CMD) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

sync-version:
	cd $(VSCODE_EXTENSION_DIR) && npm version "$(VERSION)" --no-git-tag-version --allow-same-version

setup: toolchain-install
	$(GO_CMD) mod download
	$(MAKE) toolchain-check
	@echo "Toolchain ready. Use: make ci"

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
			GOOS=$$GOOS GOARCH=$$GOARCH $(GO_CMD) build -ldflags "$(RELEASE_GO_LDFLAGS)" -o "$$output_dir/$(BINARY_NAME)$$ext" $(CMD_PATH); \
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
			CC="$(ZIG) cc -target $$target" CXX="$(ZIG) c++ -target $$target" CGO_ENABLED=1 GOOS=$$GOOS GOARCH=$$GOARCH $(GO_CMD) build -ldflags "$(RELEASE_GO_LDFLAGS)" -o "$$output_dir/$(BINARY_NAME)$$ext" $(CMD_PATH); \
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
	@chmod +x .githooks/*
	@echo "Installed git hooks from .githooks"

hooks-uninstall:
	@git config --unset core.hooksPath || true
	@echo "Removed custom core.hooksPath hook configuration"

vscode-extension-install:
	cd $(VSCODE_EXTENSION_DIR) && npm ci

vscode-extension-compile:
	cd $(VSCODE_EXTENSION_DIR) && npm run compile

vscode-extension-test:
	cd $(VSCODE_EXTENSION_DIR) && npm run test:e2e

vscode-extension-package:
	mkdir -p $(DIST_DIR)
	cd $(VSCODE_EXTENSION_DIR) && npx @vscode/vsce package --out "../../$(VSCODE_EXTENSION_PACKAGE_PATH)"
