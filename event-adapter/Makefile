# SAST tooling for event-adapter.
#
# Pinned tool versions keep CI and local runs byte-identical: CI runs the exact
# same `make sast` a developer runs locally. `make sast` is the gate enforced in
# .github/workflows/ci.yml.

GOBIN_DIR := $(shell go env GOPATH)/bin

# Pinned versions. gosec must be >= v2.26 to build under Go 1.25.
GOSEC_VERSION       := v2.26.1
GOVULNCHECK_VERSION := v1.3.0
SEMGREP_VERSION     := 1.163.0

GOSEC       := $(GOBIN_DIR)/gosec
GOVULNCHECK := $(GOBIN_DIR)/govulncheck

# Gate: medium+ severity and confidence. Generated files are excluded; test
# files are not scanned (test fixtures carry fake tokens/paths that would
# otherwise trip credential rules).
GOSEC_FLAGS := -quiet -severity medium -confidence medium -exclude-generated

# semgrep: fail on medium+ (WARNING/ERROR; INFO is informational). Uses the
# curated Go security + security-audit rulesets from the Semgrep registry.
SEMGREP_FLAGS := --error --severity=WARNING --severity=ERROR --metrics=off \
                 --config=p/golang --config=p/security-audit

.PHONY: tools sast sast-gosec sast-vuln sast-semgrep test

# Install the pinned SAST toolchain. Go tools install into $(GOBIN_DIR) with no
# go.mod impact; semgrep installs via pipx. Idempotent — safe to re-run.
tools:
	go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@if command -v pipx >/dev/null 2>&1; then \
	  pipx install --force semgrep==$(SEMGREP_VERSION) && pipx inject semgrep setuptools; \
	elif command -v semgrep >/dev/null 2>&1; then \
	  echo "pipx not found, but semgrep is already on PATH — skipping"; \
	else \
	  echo "pipx not found; install semgrep manually: pipx install semgrep==$(SEMGREP_VERSION)"; exit 1; \
	fi

# Run all SAST scans. All three always run (no fail-fast) so every category is
# reported in one pass; exits non-zero if any scan finds an issue. This is the
# exact command CI enforces.
sast:
	@rc=0; g=PASS; v=PASS; s=PASS; \
	$(MAKE) --no-print-directory sast-gosec   || { rc=1; g=FAIL; }; \
	$(MAKE) --no-print-directory sast-vuln    || { rc=1; v=FAIL; }; \
	$(MAKE) --no-print-directory sast-semgrep || { rc=1; s=FAIL; }; \
	echo "==> SAST summary: gosec=$$g govulncheck=$$v semgrep=$$s"; \
	exit $$rc

# gosec: Go security static analysis (injection, weak crypto, unsafe code,
# missing timeouts, hard-coded credentials, ...).
sast-gosec:
	@test -x "$(GOSEC)" || { echo "gosec not installed — run 'make tools'"; exit 1; }
	$(GOSEC) $(GOSEC_FLAGS) ./...

# govulncheck: known CVEs in dependencies with call-graph reachability.
# Requires outbound network to https://vuln.go.dev.
sast-vuln:
	@test -x "$(GOVULNCHECK)" || { echo "govulncheck not installed — run 'make tools'"; exit 1; }
	$(GOVULNCHECK) ./...

# semgrep: rule-based SAST. Requires outbound network to the Semgrep registry
# on first run.
sast-semgrep:
	@command -v semgrep >/dev/null 2>&1 || { echo "semgrep not installed — run 'make tools'"; exit 1; }
	semgrep scan $(SEMGREP_FLAGS) .

# Convenience: unit tests (integration tests are behind -tags=integration).
test:
	go test ./...
