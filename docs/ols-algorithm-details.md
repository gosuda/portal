# OLS Algorithm Details (Temporary)

This document isolates the OLS-specific algorithm details while keeping existing main documentation unchanged.

## Scope

- `portal/policy/ols.go`
- OLS-based target selection for relay routing
- Load-aware rotation policy and burst mitigation behavior

## Core Data Model

- `OLSManager` keeps:
  - `nodes`: node metadata and load state
  - `grid`: current `n x n` mapping (`n = floor(sqrt(node count))`)
  - `l1`, `l2`: Reverse Siamese row/column tables for deterministic coordinate mapping
  - `rotation`: current rotation angle (now conservative, not fixed 90° only)

- `OLSNode` tracks:
  - `LoadScore` (EWMA-smoothed)
  - recent failure/health state (`FailureCount`, `LastFailure`, `Healthy`)
  - `LastUpdated` timestamp for stale-load rejection

## Grid Construction

1. Sort node IDs for deterministic placement.
2. Compute `n = floor(sqrt(N))`; only reconfigure when `n` changes and `n >= 2`.
3. Fill grid with healthy nodes first, then unhealthy nodes if needed.
4. Generate paired Siamese tables:
   - build the forward Siamese square (top-middle start, up-right rule, down-step collision handling)
   - mirror + complement it to obtain the reverse square
   - project reverse -> row indices, forward -> column indices

## Deterministic Target Mapping

For `(clientID, leaseID)`:

1. Hash both values into indices `i`, `j` in `[0, n)`.
2. Use `l1[i][j]`, `l2[i][j]` to get base `(row, col)`.
3. Apply current rotation transform.
4. Select node at rotated coordinate.
5. If unhealthy/recently failing, fall back to next deterministic candidate.
6. Reject loops when the hop counter reaches the per-connection limit (`RouteContext.MaxHops`).

## Load Score and Update Path

`UpdateLoad` accepts either:

- direct propagated score (`score > 0`)
- locally computed score from `NodeLoad`

Local score formula:

- weighted normalized sum:
  - active connections
  - bytes in/out
  - connection rate
  - latency
- EWMA smoothing (`alpha = 0.2`) to avoid spike-driven oscillation
- stale timestamps are ignored

## Conservative Rotation Logic (Burst Similarity Handling)

The manager compares row/column variance:

- If one direction only slightly dominates (variance ratio just above trigger), rotate by a **small angle**.
- If dominance is severe (clear burst), rotate by **90°**.

Current behavior:

- trigger when ratio exceeds `1.05`
- interpolate angle from `15°` up to `90°`
- reach full `90°` when ratio is at/above aggressive threshold (`1.5`)

This prevents overreaction when "good" and "burst-heavy" directions are similar.

## Relay Integration Notes

- OLS node IDs are aligned to relay identity key (`identity.Key()`), matching current discovery snapshot keys.
- Overlay peer sync also uses identity keys.
- This avoids ID mismatch after main-branch identity model changes.

## Tests

`portal/policy/ols_test.go` covers:

- baseline manager initialization/routing
- conservative sub-90° rotation when variance directions are similar
- full 90° rotation for severe burst imbalance

---

Temporary note: this file is intentionally separated for OLS-focused review and can be removed/reworked at final merge as requested.
