# Phase 1 Route Config Schema

| | |
|---|---|
| **Status** | Draft |
| **User story** | [PV1-004](./phase-1-user-stories.md#pv1-004-define-protected-and-skipped-path-configuration) |
| **Related** | [phase-1-topology-decision.md](./phase-1-topology-decision.md) · [phase-1-architecture.md §1](./phase-1-architecture.md#1-software-architecture) · [PRD §5.3](./PRD.md#53-declarative-route-configuration) |

## 1. Purpose

Application teams declare which routes are protected and which are skipped. The platform translates this declaration into Envoy route configuration plus `ExtProcPerRoute` overrides (per the Option B topology decision in [phase-1-topology-decision.md](./phase-1-topology-decision.md)).

Phase 1 deliberately keeps the schema minimal: no extraction rules, no body parsing, no per-route caching, no fail-open. Those land in later phases. The schema must be small enough that an application team can adopt validation by writing one YAML file.

## 2. Schema

The configuration is a single YAML document per app.

```yaml
version: v1
appId: <string>                       # required; matches the appId in encrypted contexts (PV1-003)
defaultBehavior: deny | skipped       # required; behavior for routes that match no rule. Default value: deny.
routes:                               # required; list. First match wins.
  - method: GET | POST | PUT | DELETE | PATCH | "*"
    path: <pattern>                   # see §2.1
    behavior: protected | skipped
```

**Required fields:** `version`, `appId`, `defaultBehavior`, `routes`, and each route's `method`, `path`, `behavior`.

**Validation rules** (enforced at config-load time, before any traffic is served):

- `version` must equal `v1`.
- `appId` must match the sidecar's provisioned `appId`.
- `defaultBehavior` is `deny` or `skipped`. `protected` is not a valid default because requiring a validated encrypted context for unenumerated routes would block traffic on first deploy — teams that want everything protected must list their routes explicitly (see §4).
- `routes` is a non-empty list.
- Each `method` is one of `GET`, `POST`, `PUT`, `DELETE`, `PATCH`, or `*` (any).
- Each `path` is a non-empty pattern (§2.1).
- Each `behavior` is `protected` or `skipped`.

### 2.1 Path matching

Path patterns are gitignore-style globs:

- A literal segment matches itself (`/orders`).
- `*` matches exactly one path segment (no `/`).
- `**` matches zero or more path segments (so `/path/**` matches `/path`, `/path/`, and `/path/anything/deep`).
- Patterns must start with `/`.
- Trailing-slash handling: a pattern with a trailing slash matches only paths with a trailing slash. `/orders` does not match `/orders/`.

Matching is **first-match-wins** in list order. Application teams are responsible for ordering specific rules before general rules.

## 3. Examples

### 3.1 Protected routes

```yaml
- method: GET
  path: /api/orders/*
  behavior: protected
- method: POST
  path: /api/orders
  behavior: protected
- method: "*"
  path: /api/admin/**
  behavior: protected
```

### 3.2 Skipped routes (health checks and public assets)

```yaml
- method: GET
  path: /health
  behavior: skipped
- method: GET
  path: /metrics
  behavior: skipped
- method: GET
  path: /assets/**
  behavior: skipped
- method: GET
  path: /favicon.ico
  behavior: skipped
```

### 3.3 Complete minimal example

```yaml
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /health
    behavior: skipped
  - method: GET
    path: /assets/**
    behavior: skipped
  - method: GET
    path: /api/orders/*
    behavior: protected
  - method: POST
    path: /api/orders
    behavior: protected
```

## 4. Default behavior for unmatched routes

`defaultBehavior` controls what happens to a request whose method + path matches no rule.

| Value | Meaning | When to use |
|---|---|---|
| `deny` (recommended) | Reject with `403`. | The safe default. New routes are blocked by default until explicitly listed. |
| `skipped` | Forward without validation. | Only for apps that do not handle sensitive data and want to opt out of route-by-route enumeration. |

There is no `protected` default in Phase 1. Protected requests require an encrypted context, and requiring it for unenumerated routes would block the app's first traffic the day it deploys. Teams that want everything protected must list their routes explicitly.

## 5. Distribution

In the Option B topology:

- The YAML lives in the application team's repo, alongside the app.
- Platform CI validates the schema (§2 rules) and rejects invalid configs.
- Validated configs are translated by a platform tool into:
  - Envoy `RouteConfiguration` (method + path → route).
  - `ExtProcPerRoute` overrides on each route: `disabled: true` for `skipped`, default-enabled for `protected`.
- The translated Envoy config is delivered via a `ConfigMap` (static, Phase 1) or xDS (later phases). Phase 1 uses static config — see [phase-1-topology-decision.md §5](./phase-1-topology-decision.md#5-trade-offs-and-known-risks).

The application team never writes Envoy config directly.

## 6. Adoption / DX expectations

- A new team should be able to onboard with **one YAML file** and zero custom code.
- The platform provides a CLI / CI check (`validate-routes config.yaml`) that runs schema validation locally and in CI.
- An onboarding example covering all the patterns in §3 ships under [PV1-012](./phase-1-user-stories.md#pv1-012-create-phase-1-onboarding-example).

## 7. Out of scope (Phase 1)

- Per-route fail-open behavior.
- Per-route cache TTL or cache eligibility.
- Body or query-parameter extraction rules.
- Route-to-permission mapping (the permission comes from `X-Requested-Action`, not the route).
- Cross-checking that the URL's object ID matches the decrypted `objectId`.

These appear in later phases; teams that need them today should flag the requirement during pilot.

## 8. Acceptance criteria mapping

| Acceptance criterion | Section |
|---|---|
| Protected routes with method + path pattern | §2, §3.1 |
| Skipped routes with method + path pattern | §2, §3.2 |
| Health check and public asset examples as skipped | §3.2 |
| Default behavior for unmatched routes documented | §4 |
| Schema simple enough for application teams to adopt without custom code | §2 (size), §6 (DX) |
