# Workspace Platform — NATS Authorization & Message-Flow Design

A consolidated write-up of the design conversation covering:

- How the existing chat repo splits NATS-ACL authorization vs application-layer validation (auth-service + message-gatekeeper).
- The mechanics of NATS scoped signing keys and multi-tag expansion.
- A workspace-product design that generalizes the same pattern to any number of registered apps.
- Complete pub/sub permission tables for the user flow and app flow.
- End-to-end message-flow diagrams (Core NATS + JetStream) for every scenario, including the event marketplace fanout.

---

## 1. Why we need both an ACL layer and a gatekeeper

NATS scoped signing keys can only enforce **coarse, tag-templated** rules — they don't have per-room, per-membership, or per-payload hooks. Anything that must be validated dynamically (subscription check, role check, quoted-parent same-conversation rule, siteID match, payload shape) has to live in an application-layer gatekeeper.

### The existing chat repo split

**Auth service (`auth-service/handler.go:307-314`)** signs a NATS user JWT stamped with a single tag: `account:<name>`.

**Chat repo's scoped signing key template (`docker-local/setup.sh:50-59`):**

```
--allow-sub "chat.user.{{tag(account)}}.>"
--allow-sub "chat.room.>"
--allow-sub "_INBOX.>"
--allow-sub "chat.user.presence.state.*"
--allow-pub "chat.user.{{tag(account)}}.>"
--allow-pub "_INBOX.>"
--allow-pub "chat.user.presence.*.query.batch"
--allow-pub-response
```

The template can substitute exactly one tag: `{{tag(account)}}`. NATS enforces just one thing on the send subject `chat.user.{account}.room.{roomID}.{siteID}.msg.send`: **the `{account}` in the subject matches the client's account tag.** Everything else — `{roomID}`, `{siteID}`, payload — is untrusted.

### What `message-gatekeeper` validates (that NATS can't)

From `message-gatekeeper/handler.go` `processMessage`:

1. **siteID in subject == this gatekeeper's siteID** — client can lie in the subject.
2. **`requestId` is a valid hyphenated UUID** — reply address needs it.
3. **Message ID / thread-parent ID / quoted-parent ID are valid 20-char base62.**
4. **Content non-empty (unless attachments), ≤ 20 KB.**
5. **Attachment count ≤ cap, no empty blobs, total bytes ≤ cap.**
6. **Sender is subscribed to that room** — `store.GetSubscription(account, roomID)`. NATS can't check "is alice a member of r1".
7. **Large-room post gate** — rooms above threshold: only owner/admin/bot may post top-level; thread replies exempt.
8. **Quoted-parent lives in the same conversation** — fetch parent, enforce same-room/same-thread.
9. **Server-side stamping** — sender display name, thread-parent `createdAt` + sender account, `TShow` normalization pulled from the store, not trusted from the wire.

Only after all that does gatekeeper publish a sanitized `MessageEvent` to `chat.msg.canonical.{siteID}.created` (the canonical stream). Clients can't reach that subject — it's outside `chat.user.<account>.>`.

**One-sentence framing:** NATS = coarse "is this the right account?" ACL. Gatekeeper = dynamic "is this the right account posting into a room they're in, in this site, with a valid payload?" authorization + normalization layer.

### SSO flow vs App-token flow in the chat repo

Both flows mint the same shape of JWT — same scoped signing key, same `account:<name>` tag, same 7 template rules. Only difference: the value of the `account` tag.

| Flow | Handler | Tag value | Source |
|------|---------|-----------|--------|
| SSO (`ssoToken`) | `handleSSO` (`auth-service/handler.go:168`) | `claims.Account()` — user's account from OIDC | `TokenValidator.Validate` → `pkgoidc.Claims` |
| App/session (`authToken`) | `handleSession` (`auth-service/handler.go:218`) | `strings.ReplaceAll(p.Account, ".", "_")` — bot/admin account with dots collapsed to underscores | `BotplatformValidator.Validate` → `principal.Principal` |

Bot JWT gets `chat.user.botname_shortsiteid_bot.>` as its scope; user JWT gets `chat.user.alice.>`. Same 7 rules, different account token.

---

## 2. Generalizing: scoped signing keys with multi-tag expansion

NATS scoped SK templates support **one tag key** substituted via `{{tag(key)}}`. Two important properties:

1. **Static per JWT.** Tag values are stamped at JWT-issue time. The server does not read message payloads to decide access. `{{tag(account)}}` on a JWT with `account:alice` expands **once** to `alice`.
2. **Multi-tag expansion.** If a JWT carries the same tag key multiple times (`namespace:chat`, `namespace:docs`, `namespace:calendar`), the same template line expands into **multiple** concrete rules at connect time.

Example — template line:
```
--allow-pub "cmd.app.{{tag(namespace)}}.>"
```
JWT tags: `namespace:chat, namespace:docs, namespace:calendar`.
Expands to three concrete allow-pub rules:
```
cmd.app.chat.>
cmd.app.docs.>
cmd.app.calendar.>
```

That's the mechanism that lets **one template** serve any user regardless of how many apps they're entitled to. Onboarding a new app is a tag change, not a template change, not a new signing key, not a new NATS account.

### What multi-tag expansion cannot do

- **Per-message enforcement.** ACL is evaluated at PUB time against the connection's static tag set, not payload contents.
- **"All registered apps" implicitly.** You have to actually stamp the tag for each entitled app; there's no "grant everything" wildcard beyond `>`.

---

## 3. Workspace product — subject taxonomy

**Actors**
- **User** — human at browser; SSO-authenticated; frontend uses a user-flow JWT.
- **App** — a workspace app's backend service; uses an app-flow JWT.
- **Peer app** — another app's backend; invoked when a JWT holds a `calls:<peer>` tag, or observed via `subscribes:<peer>` for marketplace events (see §4).
- **Platform** — deferred; no separate signing key in current design.

**Subject conventions**

| Verb | Subject | Direction | Purpose |
|------|---------|-----------|---------|
| `cmd` | `cmd.app.<ns>.>` | client → app | Invoke a specific action; RPC semantics |
| `resp` | `resp.app.<ns>.<reqId>` | app → caller | Async command completion; durable-safe |
| `evt` | `evt.app.<ns>.>` | app → many | Domain events; broadcast, fan-out |
| `run` | `run.app.<ns>.>` | (deferred) | External trigger surface; drop for now |
| `dlq` | `dlq.app.<ns>.>` | app-internal | Dead letter, requeue-on-failure |
| `twsp.user` | `twsp.user.<acct>.>` | app → user | Targeted push into a specific user's subtree |
| `evt.marketplace` | `evt.marketplace.<ns>.>` | app → many apps | Fan-out event marketplace |
| `_INBOX` | `_INBOX.>` (app) / `_INBOX.<acct>.>` (user) | reply plumbing | Sync req/reply mechanics |
| `$JS.API.*` / `$JS.ACK.*` | JS control surface | app → server | Create/consume/ack JetStream consumers |

### The `twsp.user.<acct>.>` asymmetry

The one grant that is **not** tag-scoped on the app side is `PUB twsp.user.>`. The reason: the app has **one** NATS connection and serves thousands of users. It doesn't get a per-user JWT. Correctness of "right recipient" is enforced by:

- **App code** on the send side: extracts recipient from event payload/subject → builds `twsp.user.<recipient>.…` → publishes.
- **NATS ACL** on the receive side: user's JWT has `SUB twsp.user.{{tag(account)}}.>` — only own subtree.

A buggy app that publishes to the wrong recipient's subtree simply doesn't reach anyone (or reaches the wrong intended user), but a leaked pub can never reach an eavesdropper.

### Why prefer `evt.app.<sourceNs>` over `cmd.app.<targetNs>` for cross-app

- **evt (loose):** Chat publishes to its own `evt.app.chat.>` — already has PUB via own namespace tag. Search subscribes with an extra `namespace:chat` tag on **search's** JWT — grants read-only observation. Smallest possible grant. Chat has no idea search exists.
- **cmd (tight RPC):** Chat wants search to reindex — needs `PUB cmd.app.search.>`, which requires chat's JWT to carry `namespace:search`. Multi-tag expansion also gives chat SUB rights on search's cmd queue. Broader grant.

**Rule of thumb:** prefer evt for cross-app coupling; reach for cmd only when the pattern is genuinely imperative and targeted.

---

## 4. The two scoped signing keys (FINAL)

> **This section is the authoritative, decide-once specification.** It supersedes
> the earlier single-`namespace`-tag sketch. The key change: an app's own identity
> and its cross-app reach are carried by **different** tags, so the template can
> express *asymmetric* own-vs-peer permissions (an earlier symmetric
> `{{tag(namespace)}}`-only template could not, which opened an impersonation/
> eavesdrop hole). The two templates below **never change again** — all
> grants, revocations, and new-app onboarding are done by editing **tag values**,
> which come from registry rows (§4.5).

### 4.0 Tag catalog

Six tags drive everything. `{{tag(key)}}` expands once per value at connect time;
a tag with **no value ⇒ its template rule silently drops**. That is the whole
mechanism — the template is a fixed superset; entitlement lives entirely in tags.

| Tag | Value | Cardinality | Flow | Source |
|-----|-------|:-----------:|------|--------|
| `account` | user id | single | user | user identity (session/bearer) |
| `namespace` | app's **own** namespace | **single** | app | app registry (identity row) |
| `calls` | an app you may invoke | multi | both | entitlement / caller registry (§4.5) |
| `subscribes` | a marketplace producer you consume | multi | app | subscription registry (§4.5) |
| `stream` | a stream you own | multi | app | provisioning |
| `consumer` | a consumer you own | multi | app | provisioning |

**Critical rule — `namespace` is single-valued = own app only.** This is the one
behavior change from the older "stamp every registered app" approach. Cross-app
reach is expressed *only* through `calls` / `subscribes`, sourced from registry
rows — never by widening `namespace`. This keeps own (full pub+sub) strictly
separate from peer (narrow), so a peer relationship can never forge your events
or read your commands.

### 4.1 User-flow scoped signing key

**Tags:** `account` (single), `calls` (multi — the user's entitled apps).

| Subject | PUB | SUB | Rationale |
|---------|:---:|:---:|-----------|
| `_INBOX.{{tag(account)}}.>` | | ✓ | Sync replies to the user's own requests. Per-account inbox; a leaked subject can't be sub'd cross-account. Client sets `nats.CustomInboxPrefix("_INBOX.<account>")`. Caller only needs SUB — `nc.Request()` doesn't PUB its own inbox. |
| `twsp.user.{{tag(account)}}.>` | | ✓ | Async pushes/progress from apps; containment for the app-side `PUB twsp.user.>`. |
| `cmd.app.{{tag(calls)}}.>` | ✓ | | Invoke entitled apps only — expands once per `calls` value. |

**Deliberately absent:**
- `SUB resp.app.<ns>.>` — `resp.app` is keyed by the **callee app's** namespace,
  not the user; subscribing a user to it would leak *other users'* responses. All
  return paths to a user are `account`-keyed (`_INBOX` + `twsp.user`) only.
- `PUB _INBOX.…` — user is caller only.
- Any JetStream — frontends never create consumers; durability is server/backend
  side (HTTP catch-up, or a backend drains and re-pushes via `twsp.user`).
- Any `evt` / `resp` / `dlq` / `evt.marketplace` — not a user's role.

### 4.2 App-flow scoped signing key

**Tags:** `namespace` (own, single), `calls` (peer apps to invoke, multi),
`subscribes` (marketplace producers, multi), `stream` / `consumer` (owned, multi).

| Subject | PUB | SUB | Rationale |
|---------|:---:|:---:|-----------|
| `_INBOX.>` | ✓ | ✓ | Symmetric — acts as caller (sub) and responder (pub). |
| `cmd.app.{{tag(namespace)}}.>` | | ✓ | Receive commands addressed to **me** (own). |
| `resp.app.{{tag(namespace)}}.>` | ✓ | | Answer commands sent to me (own). |
| `evt.app.{{tag(namespace)}}.>` | ✓ | ✓ | Own event bus (PUB + SUB replicas). |
| `dlq.app.{{tag(namespace)}}.>` | ✓ | ✓ | Own dead-letter. **Route DLQ to a separate stream** with long retention (§6.4). |
| `evt.marketplace.{{tag(namespace)}}.>` | ✓ | | Publish **my** catalog events. |
| `run.app.{{tag(namespace)}}.>` | | ✓ | **Reserved** — receive external triggers on my own namespace. No producer today; when a scheduler/webhook appears, add a separate `scoped_platform` producer key — no change here (§8). |
| `twsp.user.>` | ✓ | | Push to any addressed user; recipient chosen from the event. Safety = user-side SUB scope. |
| `cmd.app.{{tag(calls)}}.>` | ✓ | | Invoke registered **peer** apps. |
| `resp.app.{{tag(calls)}}.>` | | ✓ | Receive responses from apps I called. |
| `evt.marketplace.{{tag(subscribes)}}.>` | | ✓ | Consume catalog fan-out I'm subscribed to. |
| `$JS.API.STREAM.INFO.{{tag(stream)}}` | ✓ | | Per-stream metadata. |
| `$JS.API.STREAM.MSG.GET.{{tag(stream)}}` | ✓ | | Direct message get. |
| `$JS.API.CONSUMER.CREATE.{{tag(stream)}}.>` | ✓ | | Create consumers on owned streams. |
| `$JS.API.CONSUMER.DURABLE.CREATE.{{tag(stream)}}.>` | ✓ | | Legacy durable-create (needed by some client libs). |
| `$JS.API.CONSUMER.INFO.{{tag(stream)}}.>` | ✓ | | Consumer state. |
| `$JS.API.CONSUMER.MSG.NEXT.{{tag(stream)}}.{{tag(consumer)}}` | ✓ | | Pull scoped to owned consumer. |
| `$JS.API.CONSUMER.DELETE.{{tag(stream)}}.{{tag(consumer)}}` | ✓ | | Cleanup owned consumer (deletes a cursor, not the stream/messages). |
| `$JS.ACK.{{tag(stream)}}.{{tag(consumer)}}.>` | ✓ | | Ack pulled messages. |

**Own vs peer is asymmetric by construction:** `namespace` (own) gets full
pub+sub on its subtrees; `calls` gets only pub-cmd + sub-resp; `subscribes` gets
only sub-marketplace. A peer relationship therefore can't forge your events or
read your commands.

**Deliberately absent:**
- `PUB run.app.<ns>.>` — apps don't trigger each other; that's a platform action (§8).
- Broad `$JS.API.>` and any `$JS.API.STREAM.DELETE`/`PURGE` — stream lifecycle
  stays with the NATS team; the app never deletes/purges streams.
- Blank `PUB evt.marketplace.>` — every producer scoped to its own subtree.

### 4.3 `nsc` command form (both keys)

Generate each scoped signing key on the account, then attach its template under a
role. Escape the `$` in `$JS…` (as `\$`) so the shell doesn't expand it, and
`nsc push -a <acct>` afterward. Verify with `nsc describe account <acct>` —
confirm `namespace` appears only on own-subjects and never widens to `*`.

**User flow:**
```sh
nsc edit account --name <acct> --sk generate     # note the printed A... key
nsc edit signing-key --account <acct> --sk <USER_SK> --role user-flow \
  --allow-sub "_INBOX.{{tag(account)}}.>" \
  --allow-sub "twsp.user.{{tag(account)}}.>" \
  --allow-pub "cmd.app.{{tag(calls)}}.>"
```

**App flow:**
```sh
nsc edit account --name <acct> --sk generate     # note the printed A... key
nsc edit signing-key --account <acct> --sk <APP_SK> --role app-flow \
  --allow-pub "_INBOX.>" \
  --allow-sub "_INBOX.>" \
  --allow-sub "cmd.app.{{tag(namespace)}}.>" \
  --allow-pub "resp.app.{{tag(namespace)}}.>" \
  --allow-pub "evt.app.{{tag(namespace)}}.>" \
  --allow-sub "evt.app.{{tag(namespace)}}.>" \
  --allow-pub "dlq.app.{{tag(namespace)}}.>" \
  --allow-sub "dlq.app.{{tag(namespace)}}.>" \
  --allow-pub "evt.marketplace.{{tag(namespace)}}.>" \
  --allow-sub "run.app.{{tag(namespace)}}.>" \
  --allow-pub "twsp.user.>" \
  --allow-pub "cmd.app.{{tag(calls)}}.>" \
  --allow-sub "resp.app.{{tag(calls)}}.>" \
  --allow-sub "evt.marketplace.{{tag(subscribes)}}.>" \
  --allow-pub "\$JS.API.STREAM.INFO.{{tag(stream)}}" \
  --allow-pub "\$JS.API.STREAM.MSG.GET.{{tag(stream)}}" \
  --allow-pub "\$JS.API.CONSUMER.CREATE.{{tag(stream)}}.>" \
  --allow-pub "\$JS.API.CONSUMER.DURABLE.CREATE.{{tag(stream)}}.>" \
  --allow-pub "\$JS.API.CONSUMER.INFO.{{tag(stream)}}.>" \
  --allow-pub "\$JS.API.CONSUMER.MSG.NEXT.{{tag(stream)}}.{{tag(consumer)}}" \
  --allow-pub "\$JS.API.CONSUMER.DELETE.{{tag(stream)}}.{{tag(consumer)}}" \
  --allow-pub "\$JS.ACK.{{tag(stream)}}.{{tag(consumer)}}.>"
```

### 4.4 Compact "who does what" matrix

| Subject | User (`account`) | App own (`namespace`) | Peer via `calls` | Consumer via `subscribes` |
|---------|:----:|:------------:|:----:|:----:|
| `cmd.app.<ns>.>` | PUB | SUB | PUB | — |
| `resp.app.<ns>.>` | — | PUB | SUB | — |
| `evt.app.<ns>.>` | — | PUB + SUB | — | — |
| `evt.marketplace.<ns>.>` | — | PUB (own) | — | SUB |
| `dlq.app.<ns>.>` | — | PUB + SUB | — | — |
| `run.app.<ns>.>` | — | SUB (reserved) | — | — |
| `twsp.user.<acct>.>` | SUB (own only) | PUB (any) | — | — |
| `_INBOX.<acct>.>` (user) / `_INBOX.>` (app) | SUB | PUB + SUB | — | — |
| `$JS.API.*` / `$JS.ACK.*` | — | PUB (own stream/consumer) | — | — |

### 4.5 Entitlement registries

Every cross-principal grant is a **row in a table**. Grant = insert a row;
revoke = delete a row. auth-service reads these at mint time to compute tag
values; nothing about NATS, the templates, or the signing keys ever changes.

**`user_app_entitlements` → feeds `calls` (user flow)** — writer: install/entitlement service.
```sql
CREATE TABLE user_app_entitlements (
  user_id        TEXT NOT NULL,   -- the logged-in human
  app_namespace  TEXT NOT NULL,   -- app they may invoke
  granted_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, app_namespace)
);
```

**`app_call_grants` → feeds `calls` (app flow)** — writer: platform/admin (apps cannot self-grant).
```sql
CREATE TABLE app_call_grants (
  caller_namespace  TEXT NOT NULL,   -- app A (the caller)
  callee_namespace  TEXT NOT NULL,   -- app B (allowed to be called)
  approved_by       TEXT NOT NULL,   -- who authorized it (audit)
  approved_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (caller_namespace, callee_namespace)
);
```

**`marketplace_subscriptions` → feeds `subscribes` (app flow)** — writer: marketplace service (on subscribe).
```sql
CREATE TABLE marketplace_subscriptions (
  subscriber_namespace  TEXT NOT NULL,   -- app consuming the events
  producer_namespace    TEXT NOT NULL,   -- app whose catalog events they get
  subscribed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (subscriber_namespace, producer_namespace)
);
```

The `namespace` tag (own identity) needs no new table — it comes from the
existing app-registry row, validated against what the caller sent to `/auth`.
`stream` / `consumer` come from provisioning records. **A mint is: own namespace +
`calls` (one of the two grant tables by flow) + `subscribes` (app only) +
owned stream/consumer.** Every tag value must be validated as a single subject
token (no `.`, `*`, `>`, whitespace) before stamping — tag integrity *is* the
authorization boundary (§6.9).

#### POC vs production tag stamping

The registries (`user_app_entitlements`, `app_call_grants`,
`marketplace_subscriptions`) may not exist yet. For a POC it is acceptable to
stamp **all registered apps** into the *relationship* tags as a temporary
shortcut — because the **templates never change**, the later migration is a
one-line swap in auth-service (replace "list all apps" with a registry query),
with no `nsc` change, no key re-issue, and no client change.

| Tag | POC value | Production value | Migration |
|-----|-----------|------------------|-----------|
| `namespace` | **own app only (single)** — keep correct now | unchanged | none |
| `calls` (user) | all registered apps | `user_app_entitlements` lookup | swap one function |
| `calls` (app) | all registered apps | `app_call_grants` lookup | swap one function |
| `subscribes` | all registered apps (or empty) | `marketplace_subscriptions` lookup | swap one function |

**The one rule that must be correct even in the POC:** `namespace` is **own app
only**, never "all apps." It is structural (the own-vs-peer boundary), it's free
(the app sends its own namespace to `/auth`), and getting it wrong both reopens
producer-source forgery (§6.10) and turns the later change into a re-architecture
instead of a one-line swap. Mark each shortcut in code so it isn't forgotten:

```go
// POC: stamp all registered apps. Replace with <registry> lookup when the
// entitlement API lands. See workspacenatspermissions.md §4.5 / §6.10.
calls := allRegisteredNamespaces()
```

### 4.6 How a grant / revoke propagates

The JWT is a **frozen snapshot** of the registries at mint time; NATS enforces
exactly what's in the presented JWT. A change goes live only when a fresh JWT is
minted with the new tags and the client reconnects with it.

```
grant/revoke  →  INSERT/DELETE row            (registry service)
              →  [optional] fire "refresh now" (webhook → sidecar; or frontend re-mints)
              →  re-mint at /api/v1/auth        (auth-service re-reads registries)
              →  forceReconnect / reconnect     (NATS picks up new JWT)
              →  new pub/sub rule live
```

- **User installs an app:** frontend writes the entitlement row, then re-calls
  `/api/v1/auth` and reconnects → `cmd.app.<new>` passes immediately.
- **App-to-app / marketplace grant:** the sidecar's refresh loop re-mints on its
  schedule and reconnects — the new `calls`/`subscribes` value is picked up at the
  **next refresh** with no redeploy. For instant effect, signal the sidecar to
  re-mint now.
- **Revoke:** delete the row; the existing JWT still allows it **until it expires**
  (the exposure window = token lifetime), then the tag drops and NATS denies. For
  instant revoke, force a re-mint on that principal.

The knob controlling propagation latency is the **token lifetime** (plus an
optional force-remint for instant). Correctness never depends on it — only speed.

---

## 5. Message-flow diagrams

> **Tag-naming note:** these diagrams predate the final tag split in §4 and still
> use `namespace:` for every relationship. Map them to the final tags: a user's
> entitled apps are now `calls` (not `namespace`); an app's own identity stays
> `namespace` (single value); a peer it calls is `calls`; a marketplace producer
> it consumes is `subscribes`. The *flows* are unchanged — only the tag key that
> authorizes each hop differs.

Consistent example throughout:
- User `alice` (user-flow JWT, `account:alice`, `calls:{chat,docs,drive}`).
- App `chat` (app-flow JWT, `namespace:chat`, `stream:CMD_CHAT`, `consumer:chat-worker`, …).
- Peer app `search` (`namespace:search`; when calling chat, also `calls:chat`).
- Marketplace producer `hr`; consumers `chat`, `docs`, `drive` (each with `subscribes:hr`).

### 5.1 user → app

**Core NATS (sync req/reply)**
```
alice frontend                                       chat backend
   │ SUB _INBOX.alice.req-001                                    │
   │                                                             │
   │ PUB cmd.app.chat.sendMessage ─────────────────────────────► │ SUB cmd.app.chat.>
   │   reply-to: _INBOX.alice.req-001                            │   handler:
   │   payload: { to: bob, text: hi }                            │   — validate
   │                                                             │   — persist
   │                                                             │
   │ ◄─── PUB _INBOX.alice.req-001 ─────────────────────────── │ PUB _INBOX.alice.req-001
   │        payload: { id, ts, status:ok }                       │
```

**JetStream (durable command)**
```
alice frontend                    CMD_CHAT stream                    chat worker
   │ PUB cmd.app.chat.sendMessage ───► │                                    │
   │ ◄── JS PubAck (seq=42) ────────── │                                    │
   │                                   │ ◄── $JS.API.CONSUMER.MSG.NEXT.CMD_CHAT.chat-worker
   │                                   │ ── seq=42 ─────────────────────► │ handler runs
   │                                   │ ◄── $JS.ACK.CMD_CHAT.chat-worker.42
   │                                                                        │
   │                                            fans out:                   │
   │                                              PUB evt.app.chat.messageCreated (→ EVT_CHAT)
   │                                              PUB twsp.user.bob.chat.newMessage (→ TWSP_USER)
   │                                              PUB twsp.user.alice.chat.ack (→ TWSP_USER)
```

### 5.2 app → user

**Core NATS (transient push, only if user online)**
```
chat backend                                              bob frontend
                                                           SUB twsp.user.bob.>
   │ PUB twsp.user.bob.chat.newMessage ────────────────► │ UI reacts
   │   payload: { from: alice, text: hi, msgId }         │
```

Recipient known from event payload; code path:
```go
func onMessageCreated(evt MessageCreatedEvent) {
    for _, recipient := range evt.RoomMembers {   // account names from payload
        pub("twsp.user."+recipient+".chat.newMessage", body)
    }
}
```

**JetStream (durable push, replays on reconnect)**
```
chat backend                     TWSP_USER stream                  bob frontend
   │ PUB twsp.user.bob.chat.newMessage ──► │                                │
   │                                       │ [consumer: bob-own,            │
   │                                       │  filter: twsp.user.bob.>]      │
   │                                       │ ◄── $JS.API.CONSUMER.MSG.NEXT.TWSP_USER.bob-own
   │                                       │ ── deliver ───────────────────► │ UI
   │                                       │ ◄── $JS.ACK.TWSP_USER.bob-own.seq
```

Bob's per-user consumer is scoped by his JWT tags `stream:TWSP_USER, consumer:bob-own`. Filter subject `twsp.user.bob.>` is enforced at consumer-create time (auth-service can verify).

### 5.3 app → app

**Event-driven (loose, preferred)**
```
chat backend               EVT_CHAT stream            search backend
(namespace:chat)                                       (namespace:search + namespace:chat)
   │ PUB evt.app.chat.messageCreated ─►  │                        │
   │                                     │ [consumer: search-observer,
   │                                     │  filter: evt.app.chat.messageCreated]
   │                                     │ ◄── MSG.NEXT ─────────  │
   │                                     │ ── deliver ────────────►│ indexes
   │                                     │ ◄── $JS.ACK
   │
   │                                     On failure:               │
   │                                                               │ PUB dlq.app.search.index-failed
   │                                                               │   (→ DLQ_SEARCH stream, long retention)
```

**RPC-driven (tight, sync)**
```
chat backend                                          search backend
(namespace:chat + namespace:search)                    (namespace:search)
   │ SUB _INBOX.chat.rpc-77                                     │
   │ PUB cmd.app.search.reindex ─────────────────────────────► │ SUB cmd.app.search.>
   │   reply-to: _INBOX.chat.rpc-77                             │   handler runs
   │ ◄─── PUB _INBOX.chat.rpc-77 ──────────────────────────── │
```

**RPC-driven (async, resp-backed — durable)**
```
chat backend                       RESP_SEARCH stream         search backend
(+ namespace:search)               retention=interest, max_age=15m
   │ PUB cmd.app.search.reindex ──────────────────────────────►│  (via CMD_SEARCH)
   │   reqId: idx-77 (no reply-to)                              │
   │ create consumer, filter resp.app.search.idx-77             │
   │                                     │                      │ ...done...
   │                                     │ ◄── PUB resp.app.search.idx-77
   │                                     │ ── deliver ─────────►│ chat receives
```

### 5.4 event marketplace fanout

**Publisher registers event type in catalog; multiple consumers subscribe independently.**

```
                MARKETPLACE_EVENTS stream
                (subjects: evt.marketplace.>)
                (retention=limits, max_age=7d, max_bytes=50GB)
                            │
 hr backend ──► PUB evt.marketplace.hr.job.status ──►│
 (namespace:hr)   payload: { jobId, status, deptId } │
                            │                        │
                     ┌──────┴──────┬────────┬────────┴────────┐
                     │             │        │                  │
              [consumer:      [consumer: [consumer:       [consumer:
               chat-hr]       drive-hr]  docs-hr]         search-hr]
                     │             │        │                  │
                     ▼             ▼        ▼                  ▼
                 chat sub       drive sub  docs sub        search sub
              (namespace:chat  (…drive)  (…docs)         (…search)
               + namespace:hr)
```

**Producer authorization:**
```
--allow-pub "evt.marketplace.{{tag(namespace)}}.>"
```
`hr` can only publish under `evt.marketplace.hr.>`; impersonating another producer's subtree is ACL-denied.

**Consumer authorization:**
Each consumer needs `namespace:<publisher-ns>` on its JWT to SUB `evt.marketplace.<publisher-ns>.>`. Auth stamps `namespace:hr` on chat/drive/docs/search's JWTs when they subscribe to hr's marketplace catalog entry.

### 5.5 Subject-to-subject cascade (single user action)

For a typical "alice sends msg → bob gets notified → search indexes it" walkthrough:

```
   alice frontend                    chat backend              bob frontend / search backend

  PUB cmd.app.chat.sendMessage ──► chat SUB cmd.app.chat.>
                                         │
                                         ├── PUB evt.app.chat.messageCreated ──┬─► search SUB evt.app.chat.>
                                         │      (JS-backed for durability)     │      (via namespace:chat tag)
                                         │                                     │      → indexes
                                         │                                     │      → $JS.ACK.EVT_CHAT.search-observer.…
                                         │                                     │      → on failure: PUB dlq.app.search.…
                                         │
                                         ├── PUB twsp.user.bob.chat.newMessage ─► bob SUB twsp.user.bob.>
                                         │                                       → UI update
                                         │                                       → $JS.ACK.TWSP_USER.bob-own.…
                                         │
                                         └── PUB _INBOX.alice.req-001 (or twsp.user.alice.chat.ack)
                                                                             → alice receives ack
```

One `cmd` in triggers three outbound streams: `evt` (for anyone caring), `twsp.user.*` (for the addressed users), `_INBOX` or user's own subtree (for the caller). Each hop is a distinct subject with its own permission grant — and every non-transient hop can be JS-backed so nothing gets lost across reconnects or restarts.

### 5.6 Picking Core NATS vs JetStream

| Situation | Use |
|-----------|-----|
| Caller wants the answer now, will retry itself | Core NATS req/reply |
| Command must not be lost if backend crashes | JS on the command subject |
| Fire-and-forget event, subscribers re-derive on reconnect | Core NATS pub |
| Event any subscriber must eventually see, even after downtime | JS event stream |
| Fanout to many users, some offline | JS + per-user filtered consumer, or push into stream-backed `twsp.user.<acct>.>` |
| One-off admin RPC between services | Core NATS req/reply |
| Cross-service workflow / pipeline | JS |

---

## 6. Cross-cutting design decisions

### 6.1 Auth-service tag policy — `calls` (both flows)

**Only entitled/registered relationships, never all registered apps.** This
applies to both the user flow's `calls` (entitled apps) and the app flow's `calls`
(approved peer relationships).

| Approach | Example `calls` tags | Behavior | Verdict |
|----------|---------------|----------|---------|
| **All registered apps** | `calls:chat, calls:docs, calls:drive, calls:hr, …` | Any principal can PUB `cmd.app.*.>` for any app | ❌ Defeats "has access to app X" as a real permission |
| **Only entitled/registered** | `calls:chat, calls:docs` (alice's installed) | Alice's PUB on `cmd.app.hr.>` denied at ACL | ✅ ACL matches the product's entitlement model |

**Source:** the registries in §4.5 — `user_app_entitlements` for the user flow,
`app_call_grants` for the app flow. auth-service reads the caller's rows at mint;
that set becomes the `calls` tag values. Redis-cache the lookup. Likewise `namespace`
is **only the caller's own app** (single), and `subscribes` comes from
`marketplace_subscriptions`.

**Consequence:** granting alice access to `hr` requires her **next** JWT to include
`calls:hr`. Until then, NATS structurally denies her `cmd.app.hr.>` publishes.
Revocation is a real path (short TTL + refresh, or resolver update) — see §4.6.

### 6.2 `_INBOX.>` scoping — safe vs strictly airtight

**Default safety (weak but usually enough):**
- `_INBOX.<id>` uses NATS-generated 22-char random nuids — unguessable.
- Each subscription is bound to one connection; user A's `_INBOX.abc` sub isn't visible to anyone else.
- Publishes with no matching interest are silently dropped.

**Structural safety (recommended for user flow):**
Prefix inboxes per account: `_INBOX.<account>.>`. A leaked inbox subject cross-account is denied by the ACL regardless of who learns it.

```
User JWT:
  --allow-sub "_INBOX.{{tag(account)}}.>"     # sub-only (caller doesn't PUB _INBOX)

App JWT:
  --allow-pub "_INBOX.>"
  --allow-sub "_INBOX.>"                       # symmetric (caller + responder)
```

Client-side requirement: `nats.CustomInboxPrefix("_INBOX.alice")` in the nats.go client.

### 6.3 `allow-pub-response` vs `PUB _INBOX.>` for responders

| Threat | `PUB _INBOX.>` | `allow-pub-response` |
|--------|:--------------:|:---------------------:|
| Compromised backend injects fake replies into arbitrary user inboxes | ✅ allowed | ❌ denied |
| Backend bug publishes to wrong inbox subject | ✅ silently sent | ❌ denied |
| Queue-group instance that didn't get the request also tries to reply | ✅ can (double-reply) | ❌ denied |
| Backend proactively pings a user's inbox unsolicited | ✅ allowed | ❌ denied |

`allow-pub-response` is strictly stronger for a **pure responder** (no `nc.Request()` calls of its own). Falls back to `PUB _INBOX.>` when the same identity is also a caller.

### 6.4 DLQ retention

**Do not host DLQ inside the live event stream.** DLQ inherits every retention setting of its host stream; a 12-hour TTL on the live stream is far too short for a dead letter you actually want to investigate.

**Correct:**

| Stream | Subjects | Retention | Max age |
|--------|----------|-----------|---------|
| `EVT_<NS>` | `evt.app.<ns>.>` | limits or interest | short (hours) |
| `DLQ_<NS>` | `dlq.app.<ns>.>` | limits | long (7–30 days) |

Same reasoning for `RESP_<NS>` streams (see §7).

### 6.5 `$JS.API.>` — never grant broad

Broad `$JS.API.>` lets a caller create/delete/introspect **every** consumer in the account, including other namespaces' streams. Instead, per-endpoint + tag-scoped:

```
$JS.API.STREAM.INFO.<STREAM>
$JS.API.STREAM.MSG.GET.<STREAM>
$JS.API.CONSUMER.CREATE.<STREAM>.>
$JS.API.CONSUMER.DURABLE.CREATE.<STREAM>.>
$JS.API.CONSUMER.INFO.<STREAM>.>
$JS.API.CONSUMER.MSG.NEXT.<STREAM>.<CONSUMER>
$JS.API.CONSUMER.DELETE.<STREAM>.<CONSUMER>
$JS.ACK.<STREAM>.<CONSUMER>.>
```

Multi-tag expansion via `stream:` and `consumer:` tags gives each app the exact set of streams/consumers it owns without granting anything else.

### 6.6 Browser bearer JWT + refresh

Browsers can't hold nkey seeds, so the user flow is bearer JWT only. Design consequences:

- Short TTL is essential (say, 15 minutes).
- Client needs a refresh path before expiry (refresh at 80% of TTL, reconnect on new JWT).
- Jitter TTLs across users (`auth-service/handler.go` `WithJitter`) so a fleet minted together doesn't expire in lockstep.

### 6.7 Auth-callout on the connect path

The load model changes drastically depending on which flow uses NATS auth-callout:

- **Both flows via callout:** Auth Service is on the critical path for every browser reconnect × N sites. Different HA sizing than "issue a token once, cache in Redis."
- **User flow uses pre-minted JWTs (resolver), app flow uses callout:** load profile completely different.

Nail this down at design time — it dominates Auth Service sizing.

### 6.8 Revocation

Two keys → two independent blast radii, but only if there's an actual revocation path. NATS SK revocation requires an account JWT update propagated through the resolver. Test end-to-end; don't discover on incident day it's a manual process.

### 6.9 Namespace-tag integrity is the crown jewel

The entire tenant isolation story reduces to the integrity of the `namespace:` tag stamped on JWTs. If the App Management Service can be tricked into stamping the wrong namespace, the template happily grants it. Treat that write path as maximally sensitive:

- Audit log every tag write.
- No self-service tag mutation.
- Validate the tag set at mint time against a canonical registered-app list.
- **Validate the app token actually owns the `namespace` it claims** before
  stamping it. Never trust the `namespace` field from the `/auth` request blindly
  — if that check is missing, an app can mint a JWT for another app's namespace,
  which reopens producer-source forgery (§6.10). This is the single most important
  control on the app flow.

### 6.10 Source forgeability — what is and isn't possible (app → app)

"Source" splits into three distinct notions; only one is fully ACL-protected.
Keep them separate when reasoning about impersonation.

| "Source" | Forgeable? | Why / control |
|----------|:----------:|---------------|
| **Producer** of an `evt.app` / `resp.app` / `evt.marketplace` message | **No** | those PUB subjects are locked to the producer's **own** `namespace` tag (single value). App A (`namespace:A`) physically cannot publish under B's subtree. |
| **Caller** of a `cmd` (as claimed in the payload) | **Yes** | `cmd.app.B` names the *target*, not the *sender*; any app with `calls:B` may publish it, and the response path is keyed by callee + reqId, so the subject never proves *which* app called. |
| **`ce-source`** CloudEvent header / any payload `source` field | **Yes** | NATS never inspects the message body; any publisher can stamp any value. |

**The linchpin: `namespace` = own, single-valued.** As long as that holds, no app
can impersonate another app's *published output*. This is why §6.9 treats the
namespace check as the crown jewel, and why the POC shortcut (§4.5) is safe — it
widens only `calls`/`subscribes`, never `namespace`, so producer-forgery stays
closed throughout.

**The POC shortcut does NOT open forgery — it opens over-reach.** Stamping
`calls`/`subscribes` = all apps lets any app *invoke* any app (`pub cmd.app.<x>`)
and *eavesdrop* on any app's responses / marketplace (`sub resp.app.<x>.>`,
`sub evt.marketplace.<x>`). That is an authorization / confidentiality problem,
not identity forgery. Scoping those tags to the registries closes it completely:

| Concern | POC (all-apps) | After registry scoping | Fixed by the code change? |
|---------|:---:|:---:|:---:|
| Forge another app's **output** (evt/resp/marketplace) | impossible | impossible | n/a — never open (`namespace`) |
| **Unauthorized invocation** (call any app) | open | closed | ✅ yes |
| **Eavesdrop** on others' resp / marketplace | open | closed | ✅ yes |
| Spoof **caller identity** inside a `cmd` payload | possible (large pool) | possible (small pool) | ⚠️ reduced, not eliminated |

**Two rules that follow:**

1. **Consumers must derive producer identity from the SUBJECT, never from
   `ce-source` or a payload `source` field.** The subject's `namespace` segment is
   ACL-enforced; the payload is not.
2. **If a callee must *trust* who called it**, that requires an app-layer
   verifiable credential in the payload — scoping `calls` shrinks the pool of
   possible callers but does not make the `cmd` subject carry sender identity.

---

## 7. Sizing considerations (from the review)

Rough numbers from the source deck; expand with:

- **Peak vs average.** 100/s peak against 69/s daily average is unrealistic. Real peaks are 5–10× average. Undersized.
- **Steady-state disk × replication.** 6 GB/day at 12h TTL on R=3 ≈ 9 GB per server plus write-amp. Quote bytes/replica, not bytes/day.
- **Auth Service QPS.** = active-users × reconnect-rate × site-count. This is the number that decides whether a Redis cache is enough, or you need per-region resolvers.
- **Consumer sizing on the marketplace fanout.** N producers × M consumers per event type. Guard the product, not events/day.
- **Server-count implication.** 100/s × 10 sites × R=3 is trivial for a single 3-node cluster on message rate alone. A supercluster is justified by cross-site sync semantics, not throughput.

---

## 8. Deferred / open questions

- **`run.app.<ns>.>`** — external **trigger surface** (scheduler / webhook / admin "run now"); the producer is platform/infra, a different principal class from users and apps. The app-flow key already **reserves the consume side** (`SUB run.app.{{tag(namespace)}}.>`) so nothing changes when it's enabled. No producer today. When needed, introduce a separate `scoped_platform` signing key for the trigger service (`PUB run.app.<target>.>`, scoped by a "which apps may I trigger" tag) — the app template stays untouched. Do **not** grant apps `PUB run.app.*` (apps don't trigger each other).
- **Presence / typing on the user side.** Does the user need `PUB twsp.user.{{tag(account)}}.>` for presence blips? If yes, add explicitly.
- **Marketplace catalog trust.** Who writes the catalog? Any auto-registration or is registration always human-reviewed? Determines whether the SUB-tag grant is safe to issue.
- **Cross-site federation for marketplace events.** Federated model needed (a producer at site A → consumers at site B) — how does the multi-tag expansion interact with per-site NATS clusters?
- **User pull consumers on TWSP_USER.** If web clients pull directly, they need `stream:TWSP_USER` and `consumer:<theirs>` tags; who mints those and when?

---

## 9. Summary

The workspace-product authorization model is **two scoped signing keys + one entitlement service** stamping six tags (§4.0):

- **User-flow key (3 rules):** narrow — SUB own inbox + own push subtree (both `account`-keyed); PUB commands on entitled apps only (`calls`). No JetStream. One template, N apps per user, entitlement set by the `calls` tag list.
- **App-flow key:** own identity (`namespace`, single) with full pub+sub on its own subtrees; cross-app reach split into `calls` (invoke peers) and `subscribes` (consume marketplace); owned `stream`/`consumer` for JetStream. One template serves every app; onboarding is a tag change.

Because own vs peer use different tags, the template expresses **asymmetric**
permissions — a peer relationship can never forge your events or read your
commands. The templates are frozen; all change is a **registry row** (§4.5) that
takes effect on the next mint-refresh-reconnect (§4.6).

Every dynamic policy check that NATS can't do (subscription check, room-membership check, per-payload rules, quoted-parent same-conversation, siteID match) lives in an application-layer gatekeeper — exactly the split the chat repo already demonstrates with `auth-service` + `message-gatekeeper`.

Blast radius is deliberately compartmentalized:

- User bug / compromise → only that user's own inbox/push subtrees + entitled `cmd`.
- App bug / compromise → only that app's own namespace + owned streams; cross-user containment via user JWTs.
- Cross-app is opt-in via `calls` / `subscribes` tags, granted per registry row.
- Marketplace is publisher-scoped (`namespace` on PUB, `subscribes` on SUB) at both ends.

Anything not covered by the ACL — recipient-correctness on `twsp.user.>` pubs, command validity, membership authorization — is enforced by app-side code, using the event payload as the source of truth for "which user does this belong to."
