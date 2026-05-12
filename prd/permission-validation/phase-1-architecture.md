# Permission Validation Phase 1 — Sidecar Architecture

This document captures the Phase 1 sidecar **software architecture** and **data flow**, derived from [phase-1-user-stories.md](./phase-1-user-stories.md). Each component and step is annotated with the user story it satisfies.

## 1. Software Architecture

```mermaid
flowchart LR
    subgraph Client["Client (Browser / App)"]
        UI[UI Layer]
    end

    subgraph Platform["Platform Services"]
        AM[Access Management API]
        PCS[Permission Checking Service]
        CRED[(App Credential Store<br/>symmetric keys by keyId)]
    end

    subgraph AppPod["Application Pod (K8s)"]
        direction TB
        subgraph Sidecar["Sidecar (Phase 1)"]
            direction TB
            RM[Route Matcher<br/>PV1-005]
            HX[Header Extractor<br/>PV1-006]
            DEC[Context Decryptor<br/>PV1-007]
            BLD[Permission Request Builder<br/>PV1-008]
            ENF[Decision Enforcer<br/>PV1-009]
            MET[SRE Metrics<br/>PV1-010]
            CFG[(Route Config<br/>protected / skipped<br/>PV1-004)]
        end
        BE[Application Backend]
    end

    UI -- "objectId, objectType" --> AM
    AM -- "encryptedContext + plainPermissions" --> UI

    UI -- "request + headers<br/>(SSO, encCtx, action)" --> RM
    RM --> HX --> DEC
    DEC -. "fetch key by keyId" .-> CRED
    DEC --> BLD --> ENF
    BLD -- "objectId, objectType, permission<br/>+ SSO header" --> PCS
    PCS -- "allow / deny" --> ENF
    ENF -- "allow → forward" --> BE
    ENF -- "deny / error → 403" --> UI
    RM -- "skipped route → forward" --> BE
    BE -- "response" --> ENF --> UI

    RM -. emits .-> MET
    HX -. emits .-> MET
    DEC -. emits .-> MET
    ENF -. emits .-> MET
    PCS -. latency .-> MET
```

### Component responsibilities

| Component | User story | Responsibility |
|---|---|---|
| Route Matcher | PV1-005 | Decide whether incoming request is protected, skipped, or unmatched, based on route config (PV1-004). |
| Header Extractor | PV1-006 | Pull SSO token, encrypted context, and requested action from headers; reject if missing/malformed. |
| Context Decryptor | PV1-007 | Decrypt + verify the authorization context (authenticated encryption, expiry, audience) using app credential. |
| Permission Request Builder | PV1-008 | Compose the PCS payload from the **decrypted** `objectId`/`objectType` and the requested `permission`; forward SSO in headers. |
| Decision Enforcer | PV1-009 | Forward on allow; return `403` on deny, timeout, or error (fail-closed). |
| SRE Metrics | PV1-010 | Emit counters and latencies for traffic, outcomes, decryption failures, and header errors. |
| Route Config | PV1-004 | Declarative list of protected and skipped routes (method + path). |
| App Credential Store | PV1-003 | Source of symmetric keys, keyed by `keyId`, provisioned at app registration. |

## 2. Data Flow — Protected Request

```mermaid
sequenceDiagram
    autonumber
    participant U as Client / UI
    participant AM as Access Mgmt API
    participant SC as Sidecar
    participant CR as App Credential
    participant PCS as Permission Checking Svc
    participant BE as App Backend

    rect rgb(240,247,255)
    note over U,AM: Pre-action: fetch authorization context (PV1-002, PV1-003)
    U->>AM: GET context (objectId, objectType)
    AM-->>U: { encryptedContext, plainPermissions }
    note right of U: plainPermissions = UI display only<br/>(NOT proof of permission)
    end

    rect rgb(245,255,245)
    note over U,BE: Action request → sidecar validation (PV1-005 … PV1-009)
    U->>SC: action request<br/>Headers: SSO, X-Auth-Context, X-Requested-Action
    SC->>SC: Route match (PV1-005)
    alt Skipped route
        SC->>BE: forward as-is
        BE-->>SC: response
        SC-->>U: response
    else Protected route
        SC->>SC: Extract headers (PV1-006)
        alt Missing / malformed headers
            SC-->>U: 403 (rejected)
        else Headers present
            SC->>CR: lookup key by keyId
            CR-->>SC: symmetric key
            SC->>SC: Decrypt + validate context<br/>(appId, exp, audience) (PV1-007)
            alt Expired / tampered / undecryptable
                SC-->>U: 403 (rejected)
            else Context valid
                SC->>PCS: POST /check<br/>body: { objectId, objectType, permission }<br/>header: SSO token (PV1-008)
                alt Granted
                    PCS-->>SC: allow
                    SC->>BE: forward request
                    BE-->>SC: response
                    SC-->>U: response
                else Denied
                    PCS-->>SC: deny
                    SC-->>U: 403 Forbidden (PV1-009)
                else Timeout / error
                    PCS--xSC: timeout / 5xx
                    SC-->>U: 403 (fail-closed, PV1-009)
                end
            end
        end
    end
    end

    note over SC: All outcomes emit metrics<br/>(allow / deny / error / decrypt-fail / header-fail) — PV1-010
```

## 3. Key Invariants

- `objectId` / `objectType` come **only** from the decrypted authorization context — never from the URL, body, or query string. Path/body extraction is explicitly out of Phase 1 scope.
- `permission` comes from the `X-Requested-Action` header — treated as **user intent**, not proof of permission (PV1-002).
- The plain permissions returned by Access Management API are **UI display data only** and never trusted by the sidecar (PV1-003).
- Default failure mode is **fail-closed**: any header, decryption, or PCS error returns `403`. Fail-open behavior is out of Phase 1 scope.
- Skipped routes bypass decryption and the Permission Checking Service entirely (PV1-004, PV1-005).
- The sidecar — not the application backend — is the enforcement point. Rejected requests must never reach the backend (PV1-009, PV1-011).

## 4. Out-of-Scope Reminders

The architecture above intentionally omits the following Phase 1 non-goals (see `phase-1-user-stories.md` → "Out Of Scope For Phase 1"):

- Decision caching and event-driven invalidation.
- Body, query, and general path-parameter extraction.
- Fail-open behavior, distributed tracing, and detailed audit logging.
- Per-route cache behavior, advanced action mapping, automatic key rotation.
- Cross-checking that the application path's object ID matches the decrypted `objectId`.
