# Branch Comparison: feature/ols-l7-load-balancing vs origin/main

## Scope
- Rebases the feature branch on the latest `origin/main` while preserving the WireGuard overlay, I2P discovery path, and OLS-aware relay routing changes.
- Resolves merge conflicts across relay startup, discovery descriptors, transport constants, and environment variables so both branches now share a single coherent contract.
- Adds a regression-focused mock test (`portal/ols/engine_test.go`) to validate that OLS routing forwards Tenant TLS traffic to a lighter peer when conditions diverge.

## Behavioral deltas
| Area | origin/main | feature branch |
| --- | --- | --- |
| Relay bootstrap & discovery | Static round-robin ordering, no overlay sync | OLS-based ordering with overlay peer sync plus WireGuard descriptor propagation |
| Admin CLI flags | No WireGuard / I2P options | Configurable WireGuard private key, overlay endpoint, and I2P proxy defaults |
| Datagram transport errors | Unexported decoding errors | Shared exported errors so SDK/relay tests can assert specific failure cases |
| OLS regression coverage | None | New mock test ensures `Engine.RouteConn` proxies requests to the computed peer when `PeerDialer` resolves it |

## Verification hooks
- New test: `go test ./portal/ols -run TestEngineRouteConnForwardsForOLSTarget`
- Existing suites remain unchanged (CI entrypoints: `make vet`, `make lint`, `make test`, `make vuln`).

## Next steps
- Run the CI stack above once the branch is ready for PR review.
- Extend the mock peer harness to cover failure/rotation paths for additional confidence.
