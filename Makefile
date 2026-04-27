.PHONY: tidy test gen build install install-script clean e2e e2e2 e2e3 e2e4 e2e5 e2e6 e2e7 e2e8 e2e9 e2e10-modal e2e10-daytona e2e10-oidc e2e11-freshinstall e2e12-in-session-ux e2e18-lsp e2e18-webfetch e2e-all python-protos python-test release release-host release-check

tidy:
	@for m in core runtime proto server cli tui sdk mcp; do \
		(cd $$m && go mod tidy) || exit 1; \
	done

test:
	@for m in core runtime proto server cli tui sdk mcp; do \
		if [ -f "$$m/go.mod" ] && find "$$m" -maxdepth 2 -name "*.go" | grep -q .; then \
			(cd $$m && go test ./...) || exit 1; \
		fi; \
	done

gen:
	@cd proto && buf generate

# Version stamping. VERSION is whatever `git describe --tags` returns
# (or 0.0.0-dev when there are no tags yet); COMMIT/DATE are derived
# from the working tree. All three are surfaced through
# core/version.{Version,Commit,BuildDate} via -ldflags below, and
# `gil --version` (and the gild/giltui/gilmcp equivalents) prints them
# verbatim.
#
# The fallbacks are intentional: contributors building from a tarball
# without git history still get a sensible "0.0.0-dev (unknown,
# unknown)" line rather than a build error. Release builds (driven by
# .goreleaser.yaml) plumb the same -X targets through goreleaser's
# template, so the same stamping mechanism covers both make build and
# tagged releases.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X 'github.com/mindungil/gil/core/version.Version=$(VERSION)' \
           -X 'github.com/mindungil/gil/core/version.Commit=$(COMMIT)'   \
           -X 'github.com/mindungil/gil/core/version.BuildDate=$(DATE)'

build:
	@mkdir -p bin
	@cd cli    && go build -ldflags "$(LDFLAGS)" -o ../bin/gil    ./cmd/gil
	@cd server && go build -ldflags "$(LDFLAGS)" -o ../bin/gild   ./cmd/gild
	@cd tui    && go build -ldflags "$(LDFLAGS)" -o ../bin/giltui ./cmd/giltui
	@cd mcp    && go build -ldflags "$(LDFLAGS)" -o ../bin/gilmcp ./cmd/gilmcp

install: build
	@install -m 0755 bin/gil    /usr/local/bin/gil    2>/dev/null || sudo install -m 0755 bin/gil    /usr/local/bin/gil
	@install -m 0755 bin/gild   /usr/local/bin/gild   2>/dev/null || sudo install -m 0755 bin/gild   /usr/local/bin/gild
	@install -m 0755 bin/giltui /usr/local/bin/giltui 2>/dev/null || sudo install -m 0755 bin/giltui /usr/local/bin/giltui
	@install -m 0755 bin/gilmcp /usr/local/bin/gilmcp 2>/dev/null || sudo install -m 0755 bin/gilmcp /usr/local/bin/gilmcp
	@echo "Installed gil, gild, giltui, gilmcp to /usr/local/bin"

# install-script: syntax-check scripts/install.sh and (optionally) serve it
# locally for end-to-end smoke testing of the curl pipe. Useful while
# iterating on the installer itself.
#
#   make install-script              # syntax-check only
#   make install-script SERVE=1      # also serve scripts/ on :8080
#
# In the SERVE=1 mode, point another shell at it with:
#   curl -fsSL http://localhost:8080/install.sh | bash
install-script:
	@bash -n scripts/install.sh && echo "scripts/install.sh: syntax OK"
	@if [ "$(SERVE)" = "1" ]; then \
		echo "Serving scripts/ on http://localhost:8080 — Ctrl-C to stop"; \
		cd scripts && python3 -m http.server 8080; \
	fi

e2e: build
	@bash tests/e2e/phase01_test.sh

e2e2: build
	@bash tests/e2e/phase02_test.sh

e2e3: build
	@bash tests/e2e/phase03_test.sh

e2e4: build
	@bash tests/e2e/phase04_test.sh

e2e5: build
	@bash tests/e2e/phase05_test.sh

e2e6: build
	@bash tests/e2e/phase06_test.sh

e2e7: build
	@bash tests/e2e/phase07_test.sh

e2e8: build
	@bash tests/e2e/phase08_test.sh

e2e9: build
	@bash tests/e2e/phase09_test.sh

e2e10-modal: build
	@bash tests/e2e/phase10_modal_test.sh

e2e10-daytona: build
	@bash tests/e2e/phase10_daytona_test.sh

e2e10-oidc: build
	@bash tests/e2e/phase10_oidc_test.sh

e2e11-freshinstall: build
	@bash tests/e2e/phase11_freshinstall_test.sh

e2e12-in-session-ux: build
	@bash tests/e2e/phase12_in_session_ux_test.sh

e2e18-lsp: build
	@bash tests/e2e/phase18_lsp_test.sh

e2e18-webfetch: build
	@bash tests/e2e/phase18_webfetch_test.sh

e2e18-plan: build
	@bash tests/e2e/phase18_plan_test.sh

e2e-all: e2e e2e2 e2e3 e2e4 e2e5 e2e6 e2e7 e2e8 e2e9 e2e10-modal e2e10-daytona e2e10-oidc e2e11-freshinstall e2e12-in-session-ux e2e18-plan e2e18-lsp e2e18-webfetch

# --- release ---------------------------------------------------------------
# `make release` builds the full 4-binary x 4-platform matrix locally via
# GoReleaser snapshot mode. Nothing is published. Use this to verify the
# release config before tagging. Requires `goreleaser` on PATH (see
# .goreleaser.yaml comments / https://goreleaser.com/install/).
release:
	@command -v goreleaser >/dev/null 2>&1 || { \
		echo "goreleaser not found on PATH"; \
		echo "install: https://goreleaser.com/install/"; \
		exit 1; \
	}
	@goreleaser release --snapshot --clean --skip=publish

# Faster snapshot for the host platform only — useful while iterating
# on the .goreleaser.yaml itself.
release-host:
	@command -v goreleaser >/dev/null 2>&1 || { \
		echo "goreleaser not found on PATH"; exit 1; \
	}
	@TARGETS="$$(go env GOOS)/$$(go env GOARCH)" \
		goreleaser release --snapshot --clean --skip=publish --single-target

# Verify the snapshot produced the expected artifacts under dist/.
release-check:
	@bash tests/release/check_artifacts.sh

clean:
	@rm -rf bin dist

# --- Python (gil_atropos) -------------------------------------------------

# Compile gRPC stubs into python/gil_atropos/proto/ (requires grpcio-tools).
python-protos:
	@cd python/gil_atropos && python3 -m gil_atropos.compile_protos --proto-root ../../proto

# Smoke tests for the Python adapter (requires pytest).
python-test:
	@cd python/gil_atropos && python3 -m pytest tests -v
