# Event-Adapter Upload Guide Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a storage-provider-neutral file upload developer guide that explains how to combine request-reply, direct HTTP upload, and asynchronous JetStream completion with the existing `event-adapter`.

**Architecture:** Keep the adapter behavior unchanged and document the upload workflow as a composition of the existing request-reply and JetStream models. Add a dedicated upload guide under `prd/event-adapter/` and link to it from the general app developer guide where presign requests are already introduced.

**Tech Stack:** Markdown documentation, existing `prd/event-adapter` docs, git.

---

## Task 1: Add the upload-specific guide

**Files:**
- Create: `prd/event-adapter/file-upload-app-developer-guide.md`
- Test: manual review of the new guide content against `docs/superpowers/specs/2026-06-08-event-adapter-upload-flow-design.md`

- [ ] **Step 1: Draft the guide structure**

Write sections for:
- why upload is a split-protocol flow
- end-to-end sequence
- presign request-reply contract
- direct HTTP upload responsibilities
- `file.uploaded` event contract
- correlation and idempotency rules
- failure handling
- testing checklist

- [ ] **Step 2: Add provider-neutral examples**

Include concrete examples for:
- a `com.workspace.files.presign.request` CloudEvent
- a representative `com.workspace.files.presign.reply` JSON body
- a `com.workspace.files.uploaded` CloudEvent
- request-route and JetStream-route YAML snippets

- [ ] **Step 3: Review the guide against the approved spec**

Check that the guide:
- does not introduce a platform `uploadId`
- requires the client to echo `objectKey` or `objectUrl`
- keeps byte upload outside the sidecar
- keeps `file.uploaded` as a client-published asynchronous event

## Task 2: Link the general app guide to the upload guide

**Files:**
- Modify: `prd/event-adapter/app-developer-guide.md`
- Test: manual review of navigation and wording

- [ ] **Step 1: Add an upload-flow callout near the request-reply section**

Update the existing request-reply guidance so the file upload example points to
the dedicated upload guide for the full multi-phase workflow.

- [ ] **Step 2: Add a related-docs link**

Add the new upload guide to the header or nearby navigation so app teams can
find it from the general guide without reading the whole document first.

- [ ] **Step 3: Review for consistency**

Verify the terminology matches the new guide:
- request-reply for presign
- direct HTTP for byte transfer
- JetStream for `file.uploaded`

## Task 3: Verify and commit the documentation update

**Files:**
- Modify: `docs/superpowers/plans/2026-06-08-event-adapter-upload-guide.md`
- Create: `prd/event-adapter/file-upload-app-developer-guide.md`
- Modify: `prd/event-adapter/app-developer-guide.md`

- [ ] **Step 1: Run a documentation sanity check**

Run:
- `git diff -- prd/event-adapter docs/superpowers/plans/2026-06-08-event-adapter-upload-guide.md`

Expected:
- diff only shows the new upload guide, the general-guide link/update, and this
  plan file

- [ ] **Step 2: Commit**

```bash
git add docs/superpowers/plans/2026-06-08-event-adapter-upload-guide.md \
  prd/event-adapter/file-upload-app-developer-guide.md \
  prd/event-adapter/app-developer-guide.md
git commit -m "docs(event-adapter): add file upload developer guide"
```
