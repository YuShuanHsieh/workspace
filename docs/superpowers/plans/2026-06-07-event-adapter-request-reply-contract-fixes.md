# Event-Adapter Request-Reply Contract Fixes Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Tighten the request-reply contract so request routes reject unsupported match keys and sidecar-generated error replies preserve request identity.

**Architecture:** Keep the JetStream event path unchanged. Narrow the request-route schema and validation around the responder's type-only matching behavior, and thread the incoming request event into the self-generated error-reply builder so reply metadata matches the documented contract.

**Tech Stack:** Go 1.25, `testing`, CloudEvents SDK, YAML schema/validation, existing `event-adapter` internal packages.

---

### Task 1: Reject unsupported request-route match keys

**Files:**
- Modify: `event-adapter/internal/config/schema.go`
- Modify: `event-adapter/internal/config/validate.go`
- Modify: `event-adapter/internal/router/matcher.go`
- Test: `event-adapter/internal/config/schema_test.go`
- Test: `event-adapter/internal/config/validate_test.go`
- Test: `event-adapter/internal/router/matcher_test.go`

**Step 1: Write the failing tests**

- Add a parse/validation test showing `requests.routes[*].match.subject` and `match.source` are rejected.
- Add a matcher/config test proving request routes still match by `type` only after the schema tightening.

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/config ./internal/router -run 'Test(Parse|Validate|NewRequests|RequestMatcher)' -v`
Expected: FAIL because request routes still reuse the broader event `MatchConfig`.

**Step 3: Write the minimal implementation**

- Introduce a request-specific match struct that only exposes `type`.
- Update request-route validation and request matcher construction to use the narrowed struct.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/config ./internal/router -run 'Test(Parse|Validate|NewRequests|RequestMatcher)' -v`
Expected: PASS

### Task 2: Preserve request identity in self-generated error replies

**Files:**
- Modify: `event-adapter/internal/cloudevent/response.go`
- Modify: `event-adapter/internal/responder/responder.go`
- Test: `event-adapter/internal/cloudevent/response_test.go`
- Test: `event-adapter/internal/responder/responder_test.go`

**Step 1: Write the failing tests**

- Add a CloudEvent unit test proving self-generated error replies can stamp `causationid`, copy `correlationid`, and use a deterministic ID derived from the triggering request.
- Add responder tests covering parse/no-route error replies with request-aware metadata where the incoming event is available.

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/cloudevent ./internal/responder -run 'TestBuildErrorReply|TestHandle' -v`
Expected: FAIL because `BuildErrorReply` currently has no input event context and responder-generated 404 replies lack request linkage.

**Step 3: Write the minimal implementation**

- Change the error-reply builder to accept the incoming request event when available.
- Reuse request-derived metadata for deterministic IDs and extensions while keeping parse-failure replies working when no parsed request exists.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/cloudevent ./internal/responder -run 'TestBuildErrorReply|TestHandle' -v`
Expected: PASS

### Task 3: Verify the full module and prepare the PR

**Files:**
- Modify: `event-adapter/AGENTS.md` only if the contract wording needs alignment with the implementation

**Step 1: Run full verification**

Run:
- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `gofmt -l .`

Expected: all green, with no newly introduced formatting issues.

**Step 2: Commit**

```bash
git add docs/superpowers/plans/2026-06-07-event-adapter-request-reply-contract-fixes.md event-adapter
git commit -m "fix(event-adapter): tighten request-reply contract"
```

**Step 3: Push and open PR**

- Push branch `codex/request-reply-contract-fixes`
- Open a draft PR summarizing the two contract fixes and the verification results
