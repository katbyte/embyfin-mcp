# recipes use bash for pipefail support (ubuntu's default sh is dash)
SHELL := /bin/bash

GIT_COMMIT=$(shell git describe --always --long --dirty)
# falls back to dev when there is no tag yet (the || must wrap git, not sed, or it never fires)
GIT_VERSION=$(shell (git describe --tags --dirty 2>/dev/null || echo dev) | sed 's/-\([0-9]*\)-g/+\1@g/')
TEST_TIMEOUT?=15m
empty:=
space:=$(empty) $(empty)

# dev tool binaries are built into .tools/bin (gitignored) from the versions pinned in
# .tools/go.mod (and .tools/actionlint/go.mod) - the single source of truth for make and CI;
# dependabot keeps them updated.
# overridable because binaries cannot execute from a noexec mount (e.g. an smb checkout):
# make TOOLS_BIN=~/.cache/embyfin-mcp/bin lint
TOOLS_BIN?=.tools/bin
ACTIONLINT=$(TOOLS_BIN)/actionlint
GOFUMPT=$(TOOLS_BIN)/gofumpt
GOLANGCI_LINT=$(TOOLS_BIN)/golangci-lint

# non-Go tools also live in .tools/bin at pinned versions, but the pins are here (dependabot
# cannot bump them): shellcheck is a static haskell binary downloaded from its github release,
# yamllint is python installed into a repo-local venv. both rebuild when this makefile changes.
SHELLCHECK_VERSION=v0.11.0
YAMLLINT_VERSION=1.38.0
ZIZMOR_VERSION=v1.30.1
SHELLCHECK=$(TOOLS_BIN)/shellcheck
YAMLLINT=$(TOOLS_BIN)/yamllint
ZIZMOR=$(TOOLS_BIN)/zizmor

# golangci-lint with the azproviderlint module plugin compiled in (.tools/.custom-gcl.yml);
# lint runs use this binary, the plain go.mod one exists to bootstrap `golangci-lint custom`
GOLANGCI_LINT_MODULES=$(TOOLS_BIN)/golangci-with-modules

# one rule builds any Go tool: the import path comes from the tool directives in .tools/go.mod
# (via go list tool), so the makefile never repeats it - add a tool there and a variable above
$(TOOLS_BIN)/%: .tools/go.mod .tools/go.sum
	@echo "==> building $* (version pinned in .tools/go.mod)..."
	@mkdir -p $(TOOLS_BIN)
	@cd .tools && go build -o $(abspath $@) $$(go list tool | grep "/$*$$")

# actionlint lives in its own module (.tools/actionlint/go.mod): it pins an older go.yaml.in/yaml/v4
# release candidate than golangci-lint's dependencies and does not compile against the newer one
$(ACTIONLINT): .tools/actionlint/go.mod .tools/actionlint/go.sum
	@echo "==> building actionlint (version pinned in .tools/actionlint/go.mod)..."
	@mkdir -p $(TOOLS_BIN)
	@cd .tools/actionlint && go build -o $(abspath $@) $$(go list tool)

# explicit rules take precedence over the pattern rule above for the non-Go tools and actionlint.
# `golangci-lint custom` always writes to .tools/bin (destination in .custom-gcl.yml), so move
# the result when TOOLS_BIN points elsewhere (e.g. a local dir because the checkout is noexec)
$(GOLANGCI_LINT_MODULES): .tools/.custom-gcl.yml $(GOLANGCI_LINT)
	@echo "==> building golangci-lint with plugins (versions pinned in .tools/.custom-gcl.yml)..."
	@cd .tools && mkdir -p bin && $(abspath $(GOLANGCI_LINT)) custom
	@if [ "$(abspath $(TOOLS_BIN))" != "$(abspath .tools/bin)" ]; then mv .tools/bin/golangci-with-modules $@; fi

$(SHELLCHECK): makefile
	@echo "==> downloading shellcheck $(SHELLCHECK_VERSION)..."
	@mkdir -p $(TOOLS_BIN)
	@os=$$(uname | tr 'A-Z' 'a-z'); arch=$$(uname -m); [ "$$arch" = "arm64" ] && arch=aarch64; \
		curl -sSfL "https://github.com/koalaman/shellcheck/releases/download/$(SHELLCHECK_VERSION)/shellcheck-$(SHELLCHECK_VERSION).$$os.$$arch.tar.xz" \
		| tar -xJ -O shellcheck-$(SHELLCHECK_VERSION)/shellcheck > $@ && chmod +x $@

$(YAMLLINT): makefile
	@command -v python3 >/dev/null || (echo "python3 is required to install yamllint (macOS: xcode CLT; Debian/Ubuntu: apt install python3-venv)" && exit 1)
	@echo "==> installing yamllint $(YAMLLINT_VERSION) into $(TOOLS_BIN)/../venv..."
	@mkdir -p $(TOOLS_BIN)
	@python3 -m venv $(TOOLS_BIN)/../venv && $(TOOLS_BIN)/../venv/bin/pip install -q yamllint==$(YAMLLINT_VERSION) && ln -sf ../venv/bin/yamllint $@

$(ZIZMOR): makefile
	@echo "==> downloading zizmor $(ZIZMOR_VERSION)..."
	@mkdir -p $(TOOLS_BIN)
	@case "$$(uname)" in Darwin) target=apple-darwin;; *) target=unknown-linux-gnu;; esac; \
		arch=$$(uname -m); [ "$$arch" = "arm64" ] && arch=aarch64; \
		curl -sSfL "https://github.com/zizmorcore/zizmor/releases/download/$(ZIZMOR_VERSION)/zizmor-$$arch-$$target.tar.gz" \
		| tar -xz -O zizmor > $@ && chmod +x $@

default: fmt build

all: fmt build

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "  \033[36m%-24s\033[0m%s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Build
build: ## Compile embyfin-mcp with version info from git
	@echo "==> building..."
	go build -ldflags "-X github.com/katbyte/go-kt/version.GitCommit=${GIT_COMMIT} -X github.com/katbyte/go-kt/version.Version=${GIT_VERSION}"

install: ## Install embyfin-mcp into GOPATH/bin with version info from git
	@echo "==> installing..."
	go install -ldflags "-X github.com/katbyte/go-kt/version.GitCommit=${GIT_COMMIT} -X github.com/katbyte/go-kt/version.Version=${GIT_VERSION}" .

docker: ## Build the embyfin-mcp container image with version info from git
	@echo "==> building docker image..."
	docker build --build-arg VERSION=${GIT_VERSION} --build-arg COMMIT=${GIT_COMMIT} -t embyfin-mcp .

tools: $(ACTIONLINT) $(GOFUMPT) $(GOLANGCI_LINT) $(GOLANGCI_LINT_MODULES) $(SHELLCHECK) $(YAMLLINT) $(ZIZMOR) ## Install all pinned dev tools into .tools/bin

##@ SDK generation (internal/pandorest)
generate: pandorest-import pandorest-generate ## Import each service's highest-versioned spec into api-defs/<service>-<version>/, then generate lib/emby, lib/jf and lib/tmdb

pandorest-import: ## Import api-defs/<service>-openapi-<version>.json (the highest version of each) into api-defs/<service>-<version>/, applying the workarounds
	@echo "==> importing the OpenAPI specs into api-defs/<service>-<version>/..."
	go run ./internal/pandorest import

pandorest-generate: ## Generate lib/emby, lib/jf and lib/tmdb from api-defs/<service>-<version>/
	@echo "==> generating lib/emby, lib/jf and lib/tmdb from api-defs/<service>-<version>/..."
	go run ./internal/pandorest generate

pandorest-diff: ## Report what the vendored specs change against their checked-in definitions
	@go run ./internal/pandorest diff -quiet

##@ Formatting
fmt: $(GOFUMPT) $(GOLANGCI_LINT) ## Fix Go formatting (gofmt, gofumpt, goimports)
	@echo "==> Fixing source code with gofmt..."
	find . -name '*.go' | grep -v vendor | xargs gofmt -s -w
	@echo "==> Fixing source code with gofumpt..."
	find . -name '*.go' | grep -v vendor | xargs $(GOFUMPT) -w
	@echo "==> Fixing imports with golangci-lint (goimports)..."
	$(GOLANGCI_LINT) fmt -E goimports ./...

goimports: $(GOLANGCI_LINT) ## Fix imports with golangci-lint (goimports)
	@echo "==> Fixing imports with golangci-lint (goimports)..."
	$(GOLANGCI_LINT) fmt -E goimports ./...

##@ Linting & Dependencies
lint: $(GOLANGCI_LINT_MODULES) ## Check source code with the golangci linters (incl. azproviderlint)
	@echo "==> Checking source code against linters..."
	$(GOLANGCI_LINT_MODULES) run ./...

actionlint: $(ACTIONLINT) $(SHELLCHECK) ## Check GitHub workflows with actionlint (incl. shellcheck on run blocks)
	@echo "==> Checking workflows with actionlint..."
	@$(ACTIONLINT) -shellcheck=$(SHELLCHECK)

lint-fix: $(GOLANGCI_LINT_MODULES) ## Fix source code with all golangci linters
	@echo "==> Checking source code against linters (applying autofixes)..."
	$(GOLANGCI_LINT_MODULES) run --fix ./...

yamllint: $(YAMLLINT) ## Check YAML files with yamllint (config in .yamllint.yml)
	@echo "==> Checking YAML files with yamllint..."
	@$(YAMLLINT) -s .

shellcheck: $(SHELLCHECK) ## Check shell scripts with shellcheck
	@echo "==> Checking shell scripts with shellcheck..."
	@$(SHELLCHECK) scripts/*.sh

zizmor: $(ZIZMOR) ## Audit GitHub workflows for security issues with zizmor
	@echo "==> Auditing workflows with zizmor..."
	@$(ZIZMOR) .

gencheck: generate ## Check that the definitions and generated SDKs match the specs (regenerate, then diff)
	@test -z "$$(git status --porcelain -- api-defs lib/emby lib/jf lib/tmdb)" || \
		(git status --short -- api-defs lib/emby lib/jf lib/tmdb; echo; \
		echo "api-defs/, lib/emby, lib/jf or lib/tmdb is stale. Run 'make generate' and commit."; exit 1)

apicheck: ## Check that the definitions match the specs and every operation has a method in lib/emby, lib/jf and lib/tmdb
	@echo "==> Checking API coverage of lib/emby, lib/jf and lib/tmdb..."
	@go run ./internal/pandorest check -quiet

depscheck: ## Check that go.mod/go.sum and vendor/ are in sync
	@echo "==> Checking source code with go mod tidy..."
	@go mod tidy
	@git diff --exit-code -- go.mod go.sum || \
		(echo; echo "Unexpected difference in go.mod/go.sum files. Run 'go mod tidy' command or revert any go.mod/go.sum changes and commit."; exit 1)
	@echo "==> Checking source code with go mod vendor..."
	@go mod vendor
	@git diff --compact-summary --exit-code -- vendor || \
		(echo; echo "Unexpected difference in vendor/ directory. Run 'go mod vendor' command or revert any go.mod/go.sum/vendor changes and commit."; exit 1)
	@echo "==> Checking .tools/go.mod with go mod tidy..."
	@cd .tools && go mod tidy
	@git diff --exit-code -- .tools/go.mod .tools/go.sum || \
		(echo; echo "Unexpected difference in .tools/go.mod/go.sum. Run 'cd .tools && go mod tidy' and commit."; exit 1)
	@echo "==> Checking .tools/actionlint/go.mod with go mod tidy..."
	@cd .tools/actionlint && go mod tidy
	@git diff --exit-code -- .tools/actionlint/go.mod .tools/actionlint/go.sum || \
		(echo; echo "Unexpected difference in .tools/actionlint/go.mod/go.sum. Run 'cd .tools/actionlint && go mod tidy' and commit."; exit 1)
	@echo "==> Checking .tools/.custom-gcl.yml golangci-lint version matches .tools/go.mod..."
	@modv=$$(cd .tools && go list -m -f '{{.Version}}' github.com/golangci/golangci-lint/v2); \
		gclv=$$(grep '^version:' .tools/.custom-gcl.yml | awk '{print $$2}'); \
		[ "$$modv" = "$$gclv" ] || \
		(echo; echo "golangci-lint version mismatch: .tools/go.mod has $$modv but .tools/.custom-gcl.yml has $$gclv - update .custom-gcl.yml to match."; exit 1)

##@ Testing
test: build ## Run tests
	go test ./... -timeout ${TEST_TIMEOUT}

# the live suites take the backend from EMBYFIN_TEST_BACKEND (emby or jellyfin), and every
# target below runs for both unless BACKENDS narrows it: make testacc BACKENDS=jellyfin
BACKENDS?=emby jellyfin

test-integration: ## Run the SDK tests (lib/emby, lib/jf) against an already-running server
	@[ -n "${EMBYFIN_SERVER}" ] && [ -n "${EMBYFIN_TOKEN}" ] && [ -n "${EMBYFIN_BACKEND}" ] || \
		(echo 'EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN must be set; or use "make testacc"'; exit 1)
	go test -tags integration -count=1 ./integration/... -timeout ${TEST_TIMEOUT} -v

test-acceptance: ## Run the tool tests (behaviour, audits, providers) against an already-running server
	@[ -n "${EMBYFIN_SERVER}" ] && [ -n "${EMBYFIN_TOKEN}" ] && [ -n "${EMBYFIN_BACKEND}" ] || \
		(echo 'EMBYFIN_BACKEND, EMBYFIN_SERVER and EMBYFIN_TOKEN must be set; or use "make testacc"'; exit 1)
	go test -tags integration -count=1 ./acceptance/... -timeout ${TEST_TIMEOUT} -v

# each suite gets its own container per backend: the SDK tests create and delete libraries
# of their own, which would trample the tool suite's fixtures. The SDK containers sit on
# the acceptance port + 100, so both can run side by side.
# live SUITE BACKEND [EXTRA ENV] - start a container, run one suite, tear it down
define live
	@echo "==> $(1) on $(2)..."
	@set -e; \
		env=".testenv-$(1)-$(2).sh"; \
		export EMBYFIN_TEST_BACKEND=$(2) EMBYFIN_TEST_CONTAINER=embyfin-mcp-$(1)-$(2) \
			EMBYFIN_TEST_DATA=$${HOME}/.cache/embyfin-mcp/testenv/$(1)-$(2) $(3); \
		[ "$(1)" = "integration" ] && export EMBYFIN_TEST_PORT=$$(( $$( [ $(2) = emby ] && echo 18097 || echo 18096 ) + 100 )) EMBYFIN_TEST_PROXY_PORT=18180 || true; \
		scripts/testenv.sh up | grep '^export' > "$$env"; \
		trap 'st=$$?; [ $$st -eq 0 ] || scripts/testenv.sh logs; \
			scripts/testenv.sh down; rm -f '"$$env"'; exit $$st' EXIT; \
		. ./"$$env"; \
		go test -tags integration -count=1 ./$(1)/... -timeout ${TEST_TIMEOUT} -v $(4)
endef

testacc-integration: ## SDK tests in a throwaway container, for each backend
	@for b in $(BACKENDS); do $(MAKE) --no-print-directory testacc-integration-$$b || exit 1; done

testacc-integration-%:
	$(call live,integration,$*)

testacc-acceptance: ## Tool tests in a throwaway container, for each backend
	@for b in $(BACKENDS); do $(MAKE) --no-print-directory testacc-acceptance-$$b || exit 1; done

testacc-acceptance-%:
	$(call live,acceptance,$*)

testacc: testacc-integration testacc-acceptance ## Run both live suites for both backends, each in its own container

# TMDB is not a server the suites run: its sweep replays recorded answers and needs neither a
# container nor a token, and a recording needs a token and nothing else
test-tmdb: ## Sweep every TMDB GET against the recorded answers (no server, no token)
	go test -tags integration -count=1 -run 'TestTMDBSweep$$' ./integration/ -timeout ${TEST_TIMEOUT} -v

record-tmdb: ## Record the TMDB sweep against the real API (needs EMBYFIN_TMDB_TOKEN)
	@[ -n "$${EMBYFIN_TMDB_TOKEN}$${EMBYFIN_TMDB_KEY}" ] || (echo 'EMBYFIN_TMDB_TOKEN must be set to record'; exit 1)
	EMBYFIN_TEST_RECORD=1 go test -tags integration -count=1 -run 'TestTMDBSweep$$' ./integration/ -timeout ${TEST_TIMEOUT} -v

# Coverage has to span all three suites or it lies: the unit tests alone report
# a fraction for tools/, because almost everything real happens in the live
# suites behind the integration tag. Each writes binary coverage into its own
# directory and covdata merges them, which is stdlib tooling rather than a
# third-party merger.
COVERDIR?=.coverage
COVERPKG=./tools/...,./lib/...,./cli/...,./internal/...
SDKS=/lib/emby/\|/lib/jf/\|/lib/tmdb/
# (the comma leads each backend's pair: foreach joins its results with spaces, which the
# recipes strip, so a trailing one would run two backends' directories together)
COVERDIRS=$(COVERDIR)/unit$(foreach b,$(BACKENDS),,$(COVERDIR)/integration-$(b),$(COVERDIR)/acceptance-$(b))

cover: ## Run every suite with coverage and report the total
	@rm -rf $(COVERDIR)
	@mkdir -p $(COVERDIR)/unit $(foreach b,$(BACKENDS),$(COVERDIR)/integration-$(b) $(COVERDIR)/acceptance-$(b))
	@echo "==> unit..."
	@go test -count=1 -coverpkg=$(COVERPKG) ./tools/ ./cli/ ./lib/... ./internal/... \
		-args -test.gocoverdir=$(CURDIR)/$(COVERDIR)/unit >/dev/null
	@for b in $(BACKENDS); do \
		$(MAKE) --no-print-directory cover-integration-$$b || exit 1; \
		$(MAKE) --no-print-directory cover-acceptance-$$b || exit 1; \
	done
	@go tool covdata textfmt -i=$(subst $(space),,$(COVERDIRS)) -o=$(COVERDIR)/coverage.all
	@# lib/emby, lib/jf and lib/tmdb are generated, one mechanical method per operation (997 of
	@# them); the suites exercise the ones the tools rely on, so the total is for the
	@# hand-written code and the SDKs get a line of their own (the per-package
	@# figures below include them)
	@grep -v '$(SDKS)' $(COVERDIR)/coverage.all > $(COVERDIR)/coverage.out
	@(head -1 $(COVERDIR)/coverage.all; grep '$(SDKS)' $(COVERDIR)/coverage.all) > $(COVERDIR)/coverage-sdk.out
	@echo
	@go tool cover -func=$(COVERDIR)/coverage.out | tail -1
	@printf 'generated SDKs:\t\t\t\t\t\t(statements)\t%s\n' "$$(go tool cover -func=$(COVERDIR)/coverage-sdk.out | tail -1 | awk '{print $$NF}')"
	@echo "==> per package"
	@go tool covdata percent -i=$(subst $(space),,$(COVERDIRS))

cover-integration-%:
	$(call live,integration,$*,,-coverpkg=$(COVERPKG) -args -test.gocoverdir=$(CURDIR)/$(COVERDIR)/integration-$*)

cover-acceptance-%:
	$(call live,acceptance,$*,,-coverpkg=$(COVERPKG) -args -test.gocoverdir=$(CURDIR)/$(COVERDIR)/acceptance-$*)

cover-html: cover ## Run every suite with coverage and open the HTML report
	@go tool cover -html=$(COVERDIR)/coverage.out

record: ## Re-record the provider cassettes against the real TMDB/TheTVDB/OMDb, for each backend (needs EMBYFIN_TMDB_TOKEN)
	@for b in $(BACKENDS); do $(MAKE) --no-print-directory record-$$b || exit 1; done

record-%:
	@echo "==> recording against the real providers (this hits the network)..."
	$(call live,acceptance,$*,EMBYFIN_TEST_RECORD=1)
	$(call live,integration,$*,EMBYFIN_TEST_RECORD=1)

record-check: ## Check the provider cassettes still match the real APIs, without rewriting them
	@for b in $(BACKENDS); do \
		echo "==> verifying cassettes against the real providers (this hits the network)..."; \
		$(MAKE) --no-print-directory record-check-$$b || exit 1; \
	done

record-check-%:
	$(call live,acceptance,$*,EMBYFIN_TEST_VERIFY=1)

testenv-up: ## Start and seed a throwaway media server (EMBYFIN_TEST_BACKEND=emby|jellyfin)
	@scripts/testenv.sh up

testenv-down: ## Remove the throwaway media server (EMBYFIN_TEST_BACKEND=emby|jellyfin)
	@scripts/testenv.sh down

check-all: build test testacc lint actionlint yamllint shellcheck depscheck gencheck apicheck ## Run build + tests (incl. live) + all linters + depscheck

.PHONY: default all help fmt goimports build docker generate pandorest-import pandorest-generate pandorest-diff lint lint-fix actionlint yamllint shellcheck zizmor gencheck apicheck depscheck check-all install tools test
