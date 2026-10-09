# Dashboard host metrics — design spec

Date: 2026-10-08
Status: **draft — levantamento (feasibility)**
Repos: `virtfoundry/core`, `virtfoundry/helm-charts`

**Scope of this document:** capture the initial levantamento for surfacing Kubernetes host metrics (CPU, memory, node count, versions) on the dashboard route, and the open decisions before implementation. No code is committed yet.

---

## 1. Goal

Extend `GET /api/v1/dashboard/summary` and the `/dashboard` UI page with cluster host telemetry sourced from the Kubernetes API (nodes, and optionally metrics-server). Today the dashboard only shows tenant-scoped counts (VMs, volumes, VPCs, networks, security groups) and recent activity — there is no visibility into the underlying cluster capacity that actually backs those VMs.

**Success:** a dashboard that, alongside the existing per-tenant cards, answers "how big is the cluster that runs my VMs?" with native K8s data, without introducing a new hard dependency (Tier 1). Tier 2 (real usage) follows behind metrics-server as a documented prerequisite.

---

## 2. Non-goals

- Reimplementing the cluster view as a custom resource (`ClusterInfo` CR + operator reconciliation) — overkill for read-only telemetry; revisit only if product asks for it.
- Replacing the existing `health` / `recent_activity` logic.
- Surfacing per-tenant consumption from `metrics-server` in this slice (Tier 2 only).
- Promoting `metrics-server` to a hard chart dependency.
- Changing the multi-tenant scoping of the existing tenant cards.
- Exposing host metrics through the gRPC spike (`InstanceService`); keep REST canonical for the UI.
- Bundling KubeVirt / Multus / CDI as chart deps (existing policy).

---

## 3. Background — what exists today

### 3.1 Current dashboard path

| Layer | File | Behaviour |
|---|---|---|
| Handler | `internal/api/handler/platform_handler.go:1069` (`DashboardSummary`) | Resolves `tenantID`, actor permissions; calls service. |
| Service | `internal/service/dashboard.go:56` (`PlatformService.DashboardSummary`) | Reads only from `store.Repository`; returns counts + health + `recent_activity`. |
| Types | `internal/service/dashboard.go:14-22` (`DashboardSummary`) | `vms`, `volumes`, `vpcs`, `networks`, `security_groups`, `health`, `recent_activity`. |
| UI | `ui/src/pages/Dashboard.tsx` | Cards + health badge + recent activity; TanStack Query `refetchInterval` backs off when WS is healthy. |
| i18n | `ui/src/lib/i18n` (PT/EN parity tested in #227) | Labels must keep parity. |

### 3.2 Host-telemetry plumbing already in the tree

| Symbol | File | Status |
|---|---|---|
| `KubeVirtDriver.ListNodes` | `internal/infra/hypervisor/kubevirt.go:350` | **Dead code** — no callers. Returns `[]NodeInfo` from `CoreV1().Nodes().List`. |
| `KubeVirtDriver.GetClusterInfo` | `internal/infra/hypervisor/kubevirt.go:389` | **Dead code** — aggregates node list into `ClusterInfo` (CPU/Mem totals, node count, Up count). |
| `KubeVirtDriver.GetHostResources` | `internal/infra/hypervisor/kubevirt.go:421` | **Dead code** — wraps `GetClusterInfo` into a `map[string]interface{}`. |
| `NodeInfo{ MemoryUsed, StorageTotal }` | `internal/infra/hypervisor/types.go:71-91` | Fields declared but **not populated** by `ListNodes` (only Capacity CPU/Mem). |
| `ClusterInfo` | `internal/infra/hypervisor/types.go:83-91` | Same shape the driver already aggregates. |
| `Nodes().List` RBAC grant | `docs/rbac-contract.yaml:51-52` | **Already listed.** No contract change needed for Tier 1. |
| Typed-client read precedent | `internal/service/vks/summary.go:1-66` | Established pattern: per-call timeout (`summaryTimeout = 8 * time.Second`), QPS/Burst on the rest.Config, build a one-shot client. |
| Instance CR informer (cmux/WS) | `internal/platform/watch/` | Already feeds `power_state` events; host metrics do **not** need realtime push — polling suffices. |

### 3.3 RBAC contract guard

`internal/platform/k8s/rbac_contract_test.go:140` (`TestRBACContractMatchesTypedClientCalls`) AST-scans every typed client-go call in `cmd/` + `internal/` and fails CI if it is not listed in `docs/rbac-contract.yaml` (or removed without removing the entry). **Every new typed call must appear there before code lands** — and the helm-charts chart must grant it (verified by `helm-charts/scripts/ci/verify-api-rbac-contract.sh`).

Tier 1 requires no contract change. Tier 2 (`metrics.k8s.io`) would need a new entry.

---

## 4. Locked decisions (so far)

| Topic | Decision |
|---|---|
| Source of truth | K8s API (`core/v1/nodes`). Same architectural posture as VKS summary — typed-client reads for telemetry. Not a new CRD. |
| Surface | Extend `GET /api/v1/dashboard/summary` (additive). Keep existing fields for UI back-compat. |
| Driver boundary | Reuse `KubeVirtDriver` as the K8s-direct reader. Add `ClusterMetrics(ctx) (*ClusterMetrics, error)` next to `GetClusterInfo`. Service layer (`PlatformService`) consumes the driver; handler stays thin. |
| Memory-store mode | When `store.driver=memory`, `k8sMgr == nil` (see `internal/platform/store/open.go:33`). `ClusterMetrics` returns `(nil, nil)`; service surfaces `hosts: null` (or omitempty); UI renders an empty/disabled state. Never 500. |
| UI integration | New section under existing cards. TanStack Query — same key `queryKeys.dashboardSummary` (additive; no new round-trip). |
| Reuse, don't fork | `KubeVirtDriver` already imports `kubernetes.Interface`; we don't need a second clientset. The driver is the only path in `internal/infra/hypervisor` that talks to K8s directly outside the store. |
| Realtime | No new WS event for host metrics. `refetchInterval` already adapts to WS health. |
| Cross-repo versioning | Additive JSON field → **MINOR** bump. Bump `core` + `helm-charts` together on the same `vX.Y.Z` per `RELEASES.md`. |

## 5. Open decisions (need product input before implementation)

1. **Visibility scope.** Host info is cluster-global, not per-tenant. Today `DashboardSummary` is scoped via `tenantID`. Options:
   - (a) Always visible to any authenticated user.
   - (b) Root-only (gated by `middleware.RequireRoot` like `/tenants`).
   - (c) New permission `platform:read`, granted to tenant-admin and root.
   - **Recommendation:** (a) for capacity totals (operationally useful for Proxmox migrants) + (b) for per-node breakdown if added later.
2. **Tier 1 only, or Tier 1 + Tier 2?**
   - Tier 1 = `core/v1/nodes` only (capacity/allocatable, ready, versions). No new dependency.
   - Tier 2 = `metrics.k8s.io` for real `used` cpu/mem. Adds `metrics-server` as a documented prerequisite (`helm-charts/docs/guide/prerequisites/`); UI must degrade gracefully when absent.
   - **Recommendation:** ship Tier 1 in one slice. Tier 2 as a separate MINOR once Tier 1 is stable.
3. **Capacity vs Allocatable.** `Status.Allocatable` excludes system reservations and is the standard "what pods can use" number. `Status.Capacity` is the hardware number. **Recommendation:** report Allocatable as the primary number; show Capacity as a secondary line ("X / Y allocatable").
4. **Per-node breakdown in v1 or v2?**
   - **Recommendation:** v1 = cluster aggregate only (4 small cards + a versions strip). v2 = a "Nodes" table when metrics-server is in.
5. **Server-side cache TTL.** Dashboard polls when WS is unhealthy. A naive Node list per poll is fine (cheap, ≤ hundreds), but with metrics-server a TTL of 15–30s on the server is sensible. **Recommendation:** in-memory TTL keyed by `time.Now()` slice, 30s for v1; revisit with Tier 2.
6. **Where the aggregate lives in code.** Two options:
   - (a) `KubeVirtDriver.ClusterMetrics(ctx)` + `PlatformService` injects into summary.
   - (b) New helper in `internal/platform/k8s` (where the Manager already imports `kubernetes.Interface`).
   - **Recommendation:** (a). Mirrors the existing `GetClusterInfo` shape; keeps the driver as the single non-CR K8s reader.

---

## 6. Proposed shape (illustrative, not committed)

```go
// internal/infra/hypervisor/types.go (additive)
type ClusterMetrics struct {
    Nodes          int                `json:"nodes"`
    NodesReady     int                `json:"nodes_ready"`
    CPUCapacity    int64              `json:"cpu_capacity_cores"`     // status.capacity
    CPUAllocatable int64              `json:"cpu_allocatable_cores"`  // status.allocatable
    MemoryCapacity int64              `json:"memory_capacity_bytes"`  // bytes
    MemoryAllocatable int64           `json:"memory_allocatable_bytes"`
    KubeletVersions []string          `json:"kubelet_versions"`       // distinct
    OSImages       []string           `json:"os_images"`              // distinct (capped)
    OSArchitectures []string          `json:"os_architectures"`       // distinct
    CollectedAt    time.Time          `json:"collected_at"`
}

// internal/service/dashboard.go (additive)
type DashboardSummary struct {
    // …existing fields unchanged…
    Hosts *ClusterMetrics `json:"hosts,omitempty"` // null when memory store or Forbidden
}
```

UI: 4 new cards (`Nodes`, `Nodes Ready`, `CPU allocatable`, `Memory allocatable`) under the existing `grid`, plus a small text line `Kubelet: v1.X.Y · OS: …` and the `collected_at` as a tooltip.

---

## 7. Architecture / data flow

```
ui/src/pages/Dashboard.tsx
        │  GET /api/v1/dashboard/summary  (existing key)
        ▼
internal/api/handler/platform_handler.go (DashboardSummary)
        │  tenantID + actor perms  (existing)
        ▼
internal/service/dashboard.go (PlatformService.DashboardSummary)
        │  store reads (existing)
        │  +  s.kv.ClusterMetrics(ctx)        ← NEW (driver-only on memory store returns nil)
        ▼
internal/infra/hypervisor (KubeVirtDriver)
        │  cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})  (typed, RBAC-listed)
        ▼
K8s API server
```

No new dependency. No new RBAC contract entry for Tier 1. Driver remains the only typed-client K8s surface outside the `store` package.

---

## 8. Test plan

| Layer | Test |
|---|---|
| `internal/infra/hypervisor` | Unit test for `ClusterMetrics` using a fake `kubernetes.Interface`: empty list, mixed Ready/NotReady, multiple kubelet versions, allocatable vs capacity. |
| `internal/service` | Extend `dashboard_test.go`: `hosts == nil` when `kvm == nil` (memory mode); `hosts` populated when driver returns metrics; `DashboardSummaryHidesVMNamesWithoutVMsRead` keeps passing (no regression on permission gating). |
| `internal/platform/k8s` | Existing `rbac_contract_test.go` must still pass (no new typed call → no contract change). |
| UI | Update `lib/platform-api.ts` `DashboardSummary` type; a vitest asserting the new cards render when `hosts` is set and render the disabled state when `null`. |

Homelab gate (per AGENTS.md): validated on a Kubernetes cluster before merge. Memory store (`go run`) must boot cleanly.

---

## 9. Effort estimate

| Slice | Scope | Effort |
|---|---|---|
| **v1 — Tier 1, aggregate** | `KubeVirtDriver.ClusterMetrics`, service injection, additive `hosts` field, 4 UI cards + versions line, PT/EN i18n, tests, CHANGELOG (MINOR). | **S** — ~1–2 days |
| **v2 — Tier 1 + Tier 2** | metrics-server typed-client read, `used` fields, server-side TTL, RBAC contract entry (`metrics.k8s.io/nodes`), chart ClusterRole grant, graceful absence in UI, prerequisites doc update. | **M** — +3–5 days |
| **v3 — ClusterInfo CR + operator** | New CRD, operator reconcile loop against `core/v1/nodes`, API becomes pure CR facade. | **L** — ~1 week; revisit only on demand |

**Recommendation:** ship v1. Re-evaluate Tier 2 once v1 is live on the homelab.

---

## 11. Tier 2 implementation status (this slice)

The backend part of v2 is implemented in this branch (`feat/dashboard-improvements`). The UI integration is intentionally deferred to a follow-up slice so this commit stays minimal and reviewable.

### 11.1 Locked decisions (Tier 2 backend)

| Topic | Decision |
|---|---|
| Source of truth | `metrics.k8s.io/v1beta1/nodes` via `k8sClient.Discovery().RESTClient().Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes")`. The typed client-go does not expose `metrics.k8s.io` (the API is provided by the add-on `metrics-server`, not by client-go). The access is documented under `dynamicClient` in `docs/rbac-contract.yaml` and granted in the chart. |
| Aggregation | Sums `usage.cpu` and `usage.memory` across all items in the response. Mirrors the Tier 1 "sum across all nodes" posture. |
| Cache | Per-driver in-memory cache, TTL **30s**, key on the response itself. Cache invalidates on error so a transient 503 next poll is a fresh attempt. |
| Absence / Forbidden | Tolerated: `isMetricsAbsent` recognises 404 (API not registered), 403 (no grant), 401, 503, timeout, "connection refused", "no such host". Returns `(nil, nil)` and lets the dashboard render a degraded state. Genuine errors (parse failure, etc.) propagate. |
| Field shape | Additive: `ClusterMetrics.Usage *ClusterUsage` (omitempty). When nil, the JSON omits the field and the UI shows "metrics-server unavailable". |
| Visibility | Same as Tier 1: visible to any authenticated user. Host info is cluster-global and operationally useful. |

### 11.2 Out of scope (deferred)

- **UI**: cards keep their Tier 1 look in this slice. The follow-up will add a usage bar (`used / allocatable`) with 60% / 85% thresholds and a degraded-state badge when `usage == nil`.
- **Chart**: `helm-charts` change to grant `metrics.k8s.io/nodes` get/list and document `metrics-server` as a prerequisite. This is a cross-repo change (per `RELEASES.md` it must be released on the same `vX.Y.Z` as the core slice).
- **Per-node breakdown**: still v3 (table view).
- **Cross-repo MINOR bump**: to be applied together with the chart change.

---

## 10. References

- `AGENTS.md` — repo guardrails (CRD-first, homelab gate, SemVer 0.9.x)
- `docs/ARCHITECTURE.md` — backend layers, REST surface, persistence model
- `docs/rbac-contract.yaml` — typed-client RBAC single source of truth
- `internal/platform/k8s/rbac_contract_test.go` — guard test
- `internal/infra/hypervisor/kubevirt.go:350-430` — existing dead-code paths (`ListNodes`, `GetClusterInfo`, `GetHostResources`)
- `internal/service/vks/summary.go:1-66` — precedent for typed-client telemetry with timeout/QPS
- `internal/service/dashboard.go` — current summary shape
- `ui/src/pages/Dashboard.tsx` — current dashboard
- `RELEASES.md` — cross-repo MINOR bump process