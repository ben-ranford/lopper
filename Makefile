.PHONY: format fmt format-check gostyle lint actionlint shellcheck mod-check feature-flag feature-flag-graduate feature-flag-check dup-check suppression-check github-actions-pinning github-actions-runners automation-examples release-automation-check managed-output-check automation-integrity security vuln-check test test-lockfiledrift-head vscode-release-notes-check cyclonedx-schema-check test-leaks test-leaks-lockfiledrift-head test-race test-race-lockfiledrift-head bench-mem bench-delta bench-gate cov cov-lockfiledrift-head benchdelta-cov build manpage ci smoke demos demos-check mem-profiles release clean toolchain-check toolchain-install toolchain-install-macos toolchain-install-linux print-gosec-version tools-install setup hooks-install hooks-uninstall sync-version vscode-extension-install vscode-extension-compile vscode-extension-test vscode-extension-package

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
BUILD_CHANNEL ?= dev
RELEASE_BUILD_CHANNEL ?= release
COVERAGE_FILE ?= .artifacts/coverage.out
COVERAGE_DEFAULT_FILE ?= .artifacts/coverage-default.out
COVERAGE_LOCKFILEDRIFT_HEAD_FILE ?= .artifacts/coverage-lockfiledrift-head.out
COVERAGE_MIN ?= 98
COVERAGE_PACKAGE_MIN ?= $(COVERAGE_MIN)
LOCKFILEDRIFT_HEAD_TAG ?= lockfiledrift_head
LOCKFILEDRIFT_HEAD_PACKAGE ?= ./internal/app
GO ?= go
GO_BIN ?=
GO_TOOLCHAIN ?= go1.27.0
GO_CMD := GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO)
MANPAGE_OUT ?= docs/man/lopper.1
GOLANGCI_LINT_VERSION ?= v2.13.1
GOSTYLE_VERSION ?= v0.26.1-0.20260607232238-6ca19a9c9020
GOSEC_VERSION ?= v2.28.0
GOSEC_EXCLUDE_RULES ?= internal/gitexec/gitexec\\.go:G204;tools/regressionproof/main\\.go:G204
ACTIONLINT_VERSION ?= v1.7.12
GOVULNCHECK_VERSION ?= v1.7.1-0.20260819171436-ff4f1c5e865b
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
GO_VERSION_LDFLAGS = -X $(VERSION_PKG).version=$(VERSION) -X $(VERSION_PKG).commit=$(GIT_COMMIT) -X $(VERSION_PKG).buildDate=$(BUILD_DATE) -X $(VERSION_PKG).buildChannel=$(BUILD_CHANNEL)
RELEASE_VERSION_LDFLAGS = -X $(VERSION_PKG).version=$(VERSION) -X $(VERSION_PKG).commit=$(GIT_COMMIT) -X $(VERSION_PKG).buildDate=$(BUILD_DATE) -X $(VERSION_PKG).buildChannel=$(RELEASE_BUILD_CHANNEL)
BUILD_GO_LDFLAGS ?= $(GO_VERSION_LDFLAGS)
RELEASE_GO_LDFLAGS ?= -s -w $(RELEASE_VERSION_LDFLAGS)
TEST_VERSION_LDFLAGS = -X $(VERSION_PKG).buildChannel=$(BUILD_CHANNEL)
GO_TEST_LDFLAGS ?= $(TEST_VERSION_LDFLAGS)
GO_TEST_LDFLAGS_ARGS = $(if $(strip $(GO_TEST_LDFLAGS)),-ldflags "$(GO_TEST_LDFLAGS)")

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

feature-flag:
	$(GO_CMD) run ./tools/featureflag add --name "$(NAME)" --description "$(DESCRIPTION)"

feature-flag-graduate:
	$(GO_CMD) run ./tools/featureflag graduate --feature "$(FEATURE)"

feature-flag-check:
	$(GO_CMD) run ./tools/featureflag validate

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

github-actions-pinning:
	@./scripts/check-github-actions-pinning.sh

github-actions-runners:
	@ruby scripts/check-github-actions-runners.rb

automation-examples:
	@./scripts/check-automation-examples.sh

release-automation-check:
	@./scripts/check-release-automation.sh

managed-output-check:
	@./scripts/check-managed-output.sh

automation-integrity:
	@./scripts/check-automation-integrity.sh

security:
	$(GO_CMD) run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) -exclude-rules "$(GOSEC_EXCLUDE_RULES)" ./...

vuln-check:
	$(GO_CMD) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

test:
	@pkgs=$$(GOFLAGS=-buildvcs=false $(GO_CMD) list ./... | grep -Ev '/internal/app$$'); \
		$(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) $$pkgs
	@$(MAKE) test-lockfiledrift-head
	@python3 -m unittest scripts/vscode_release_notes_test.py
	@$(MAKE) vscode-release-notes-check

vscode-release-notes-check:
	@python3 scripts/vscode_release_notes.py --check

test-lockfiledrift-head:
	$(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) -tags "$(LOCKFILEDRIFT_HEAD_TAG)" $(LOCKFILEDRIFT_HEAD_PACKAGE)

.PHONY: fuzz-corpus-check
fuzz-corpus-check:
	$(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) ./scripts -run '^(TestParserFuzzCorpusContract|TestParserFuzzWorkflowContract)$$'

ci: fuzz-corpus-check

.PHONY: runtime-pycache-check
runtime-pycache-check:
	@if [ -d scripts/runtime/__pycache__ ]; then \
		echo "runtime Python bytecode artifacts must not be left in scripts/runtime"; \
		find scripts/runtime/__pycache__ -type f -print; \
		exit 1; \
	fi

cyclonedx-schema-check:
	$(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) ./internal/report -run '^TestCycloneDXSchema'

test-leaks:
	@pkgs=$$(GOFLAGS=-buildvcs=false $(GO_CMD) list ./... | grep -Ev '/internal/app$$'); \
		GOLEAK=1 $(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) $$pkgs
	@$(MAKE) test-leaks-lockfiledrift-head

test-leaks-lockfiledrift-head:
	GOLEAK=1 $(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) -tags "$(LOCKFILEDRIFT_HEAD_TAG)" $(LOCKFILEDRIFT_HEAD_PACKAGE)

test-race:
	@pkgs=$$(GOFLAGS=-buildvcs=false $(GO_CMD) list ./... | grep -Ev '/internal/app$$'); \
		$(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) -race $$pkgs
	@$(MAKE) test-race-lockfiledrift-head

test-race-lockfiledrift-head:
	$(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) -race -tags "$(LOCKFILEDRIFT_HEAD_TAG)" $(LOCKFILEDRIFT_HEAD_PACKAGE)

bench-mem:
	@mkdir -p $$(dirname "$(BENCH_OUTPUT)"); \
	bench_output_tmp=$$(mktemp); \
	trap 'rm -f "$$bench_output_tmp"' EXIT INT TERM; \
	if ! GOFLAGS=-buildvcs=false $(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) -run '^$$' -bench . -benchmem -count=$(BENCH_COUNT) -benchtime=$(BENCH_TIME) $(MEMORY_BENCH_PACKAGES) > "$$bench_output_tmp" 2>&1; then \
		cat "$$bench_output_tmp"; \
		exit 1; \
	fi; \
	cat "$$bench_output_tmp"; \
	cp "$$bench_output_tmp" "$(BENCH_OUTPUT)"

bench-delta:
	$(GO_CMD) run ./tools/benchdelta -base "$(BENCH_BASE_OUTPUT)" -head "$(BENCH_HEAD_OUTPUT)" -max-bytes-pct "$(MEMORY_BENCH_MAX_BYTES_PCT)" -max-allocs-pct "$(MEMORY_BENCH_MAX_ALLOCS_PCT)" -summary-out "$(MEMORY_BENCH_SUMMARY)"

export MEMORY_BENCH_BASE GO GO_BIN GO_TOOLCHAIN BENCH_COUNT BENCH_TIME MEMORY_BENCH_PACKAGES MEMORY_BENCH_MAX_BYTES_PCT MEMORY_BENCH_MAX_ALLOCS_PCT BENCH_BASE_OUTPUT BENCH_HEAD_OUTPUT MEMORY_BENCH_SUMMARY MEMORY_BENCH_STATUS MEMORY_BENCH_ENFORCE GO_TEST_LDFLAGS
bench-gate:
	@if [ -x ./scripts/bench-gate-pr-base.sh ]; then \
		./scripts/bench-gate-pr-base.sh ./scripts/bench-gate.sh; \
	else \
		./scripts/bench-gate.sh; \
	fi

.PHONY: benchdelta-cov
benchdelta-cov:
	@mkdir -p .artifacts
	@GOFLAGS=-buildvcs=false $(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) ./tools/benchdelta -covermode=atomic -coverprofile=".artifacts/benchdelta-coverage.out"
	@GOFLAGS=-buildvcs=false $(GO_CMD) run ./tools/coveragegate \
		-coverprofile=".artifacts/benchdelta-coverage.out" \
		-min=97.0 \
		-package-min=97.0 \
		-total-out=".artifacts/benchdelta-coverage-total.txt" \
		-packages-out=".artifacts/benchdelta-coverage-packages.txt" \
		-package-failures-out=".artifacts/benchdelta-coverage-package-failures.txt"

ci: benchdelta-cov

cov:
	@mkdir -p $$(dirname "$(COVERAGE_DEFAULT_FILE)")
	@pkgs=$$(GOFLAGS=-buildvcs=false $(GO_CMD) list ./... | grep -Ev '/internal/app$$|/internal/testutil$$|/internal/testsupport$$|/tools/benchdelta$$'); \
		GOFLAGS=-buildvcs=false $(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) $$pkgs -covermode=atomic -coverprofile="$(COVERAGE_DEFAULT_FILE)"
	@$(MAKE) cov-lockfiledrift-head
	@mkdir -p $$(dirname "$(COVERAGE_FILE)")
	@{ \
		awk 'NR==1 || FNR>1 { print }' "$(COVERAGE_DEFAULT_FILE)"; \
		awk 'FNR>1 { print }' "$(COVERAGE_LOCKFILEDRIFT_HEAD_FILE)"; \
	} > "$(COVERAGE_FILE)"
	@mkdir -p .artifacts
	@GOFLAGS=-buildvcs=false $(GO_CMD) run ./tools/coveragegate \
		-coverprofile="$(COVERAGE_FILE)" \
		-min="$(COVERAGE_MIN)" \
		-package-min="$(COVERAGE_PACKAGE_MIN)" \
		-total-out=".artifacts/coverage-total.txt" \
		-packages-out=".artifacts/coverage-packages.txt" \
		-package-failures-out=".artifacts/coverage-package-failures.txt"

cov-lockfiledrift-head:
	@mkdir -p $$(dirname "$(COVERAGE_LOCKFILEDRIFT_HEAD_FILE)")
	GOFLAGS=-buildvcs=false $(GO_CMD) test $(GO_TEST_LDFLAGS_ARGS) -tags "$(LOCKFILEDRIFT_HEAD_TAG)" $(LOCKFILEDRIFT_HEAD_PACKAGE) -covermode=atomic -coverprofile="$(COVERAGE_LOCKFILEDRIFT_HEAD_FILE)"

build:
	mkdir -p $(BIN_DIR)
	GOFLAGS=-buildvcs=false $(GO_CMD) build -ldflags "$(BUILD_GO_LDFLAGS)" -o $(BIN_DIR)/$(BINARY_NAME) $(CMD_PATH)

manpage:
	./scripts/generate-manpage.sh $(MANPAGE_OUT)

ci: automation-integrity format-check mod-check feature-flag-check lint actionlint shellcheck dup-check suppression-check security vuln-check test test-leaks test-race bench-gate build cov runtime-pycache-check

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
	@command -v ruby >/dev/null 2>&1 || (echo "ruby not found in PATH (required for automation integrity YAML/JSON checks)"; exit 1)
	@command -v node >/dev/null 2>&1 || (echo "node not found in PATH (required for automation integrity JavaScript syntax checks)"; exit 1)
	@node_major="$$(node -e 'console.log(process.versions.node.split(".")[0])' 2>/dev/null || echo 0)"; \
	if [ "$$node_major" -lt 20 ]; then \
		echo "Node.js 20.x or newer is required (found major version $$node_major)."; \
		echo "Install/update Node from https://nodejs.org/ or use NodeSource (see .github/workflows/ci.yml)."; \
		exit 1; \
	fi
	@command -v python3 >/dev/null 2>&1 || (echo "python3 not found in PATH (required for Python-based CI checks)"; exit 1)

toolchain-install:
	@uname_s="$$(uname -s)"; \
	case "$$uname_s" in \
		Darwin) $(MAKE) toolchain-install-macos ;; \
		Linux) $(MAKE) toolchain-install-linux ;; \
		*) echo "Unsupported OS: $$uname_s"; exit 1 ;; \
	esac

toolchain-install-macos:
	@command -v brew >/dev/null 2>&1 || (echo "homebrew not found"; exit 1)
	brew install go zig shellcheck ruby node python

toolchain-install-linux:
	@if command -v apt-get >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO apt-get update; \
		$$SUDO apt-get install -y golang-go zig shellcheck ruby nodejs python3; \
	elif command -v dnf >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO dnf install -y golang zig ShellCheck ruby nodejs python3; \
	elif command -v pacman >/dev/null 2>&1; then \
		if [ "$$(id -u)" -eq 0 ]; then SUDO=""; else SUDO="sudo"; fi; \
		$$SUDO pacman -Syu --noconfirm --needed go zig shellcheck ruby nodejs python; \
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

print-gosec-version:
	@echo $(GOSEC_VERSION)

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
		mkdir -p "$$output_dir/share/lopper/scripts"; \
		cp -R scripts/runtime "$$output_dir/share/lopper/scripts/"; \
		mkdir -p "$$output_dir/share/man/man1"; \
		./scripts/generate-manpage.sh "$$output_dir/share/man/man1/$(BINARY_NAME).1"; \
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
