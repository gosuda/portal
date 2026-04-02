# Portal Ethereum Roadmap

## Goal

Turn Portal from a self-hosted relay product that already uses Ethereum identity internally into an Ethereum-native service publishing layer:

- `wallet` owns the app or relay
- `ENS` and `DNSSEC` verify which `address` currently controls a relay or app endpoint
- `Portal` continues to provide transport, routing, and end-to-end TLS
- `L2` handles rewards, payouts, and paid access

This roadmap is intentionally incremental. It preserves the current transport model and avoids pushing dynamic lease state fully onchain.

## Current Position

Portal already has strong Ethereum alignment:

- Lease registration is authenticated with SIWE.
- Each app or relay identity already carries an `address` owner field.
- Relay and tunnel transport are already separated from identity.
- DNS + ACME + wildcard TLS already solve public HTTPS delivery.

What is still missing:

- user-facing ENS identity and discovery
- a single public identity model across UI, registry, and rewards
- an Ethereum-native rewards and payments layer
- a clear split between stable relay identity and temporary app lease endpoints

## Core Decisions

### Keep these as-is

- Keep the current raw TCP reverse transport and SNI routing model.
- Keep current tenant TLS passthrough and keyless signing.
- Keep DNS + ACME for browser-facing HTTPS.
- Keep relay-hosted wildcard domains for actual service delivery.

### Add these on top

- Use `Identity` as the single product identity contract.
- Use `address` as the canonical owner field inside that identity.
- Use ENS first for verifiable relay identity and address binding, then later for richer profiles and discovery where it adds real value.
- Use `L2 mainnet` as the default L2 for rewards and payments.
- Use offchain accounting with onchain settlement, not onchain event-by-event metering.
- Use x402 only where Portal can actually see HTTP, not across the raw passthrough data plane.
- Keep app subdomains as temporary address-authenticated leases unless persistent name ownership becomes a real product requirement.

## Product Positioning

Portal should be positioned as:

- wallet-owned infrastructure
- ENS-addressable services
- permissionless app publishing for Ethereum-native services
- a bridge between offchain apps and Ethereum identity, naming, and economics

This is not "ngrok but web3".
It is closer to "Ethereum-native app publishing and service identity".

## Architecture End State

### Transport

- Portal remains the transport and ingress layer.
- Relay routing, reverse sessions, UDP relay, and E2EE remain unchanged.

### HTTPS

- Public app access continues to use DNS hostnames such as `app.portal.example.com`.
- ACME continues to provision and renew relay root and wildcard certificates.
- ENS does not replace ACME.

### Identity

- `Identity` becomes the only stable product identity contract.
- `Identity.Name` is the service or relay identity label used by Portal.
- `Identity.Address` is the canonical owner wallet for that identity.
- UI, registry, rewards, and relay operator state all key off the same `Identity`, with `address` anchoring ownership.
- Human-friendly owner names come from ENS primary names and ENS profile data.

### Naming

- `ENSIP-17` is used for stable relay DNS names, such as `portal.example.com`.
- Canonical `.eth` relay names such as `relay.eth` are optional follow-on identity surfaces, not a requirement for initial ENS support.
- Temporary app subdomains such as `app.portal.example.com` remain Portal leases by default; when published with ENSIP-17 they prove current controlling `address`, not permanent name ownership.
- ENS-native app names such as `app.relay.eth` are deferred until Portal has a concrete need for persistent app identity beyond temporary lease hostnames.

### Economics

- Reward calculation stays offchain.
- Reward claiming and paid access settle onchain on L2 mainnet.
- Paid HTTP access can use x402.

## Roadmap

### Phase 0: Identity Cleanup

Objective:
Make `Identity` the single real identity contract across the product.

Scope:

- Keep `Identity` as the canonical contract for auth, registry, policy, rewards, claims, and grouping.
- Keep `Identity.Address` as the canonical owner field for wallet ownership and future ENS resolution.
- Keep `metadata.owner` as the user-facing owner label in frontend pages.
- Do not expose `Address` directly in frontend page UI until ENS/address display policy is finalized.
- Define a single normalization policy for `Identity.Name` and EVM `Address`.

Success criteria:

- Every public lease, relay operator, and future reward recipient can be mapped to one `Identity`.
- Every `Identity` can be mapped to one canonical owner `address`.
- No duplicate identity contracts remain in the main user path.

### Phase 1: ENS Profile UX

Objective:
Make Portal identities legible inside the product.

Scope:

- Resolve primary ENS names for `Identity.Address`.
- Resolve ENS avatars and basic profile records where available.
- Display ENS name first, checksum address second, raw metadata fallback last.
- Show relay operator identity and app identity using the same rendering rules.

Success criteria:

- Public app cards and relay/operator views show wallet-native identities instead of ad hoc labels.
- Address ownership is still verifiable even when ENS data is absent.

### Phase 2: DNS-Backed Relay ENS Presence

Objective:
Make Portal relays verifiably address-bound in the ENS ecosystem without requiring onchain ENS registration.

Scope:

- Enable DNSSEC on the relay root domain.
- Add `ENSIP-17` TXT records for the stable relay DNS name.
- Bind the relay root domain to the relay operator `address`.
- Keep relay discovery and transport state separate from relay identity: ENS proves `who`, Portal discovery continues to publish live topology.
- Do not require a canonical `.eth` name or resolver-managed ENS text records in this phase.

Important:

- ENSIP-17 is complementary to ACME, not a replacement.
- Use ENSIP-17 first for stable relay names, not short-lived lease hostnames.
- The primary value of ENS in this phase is independent verification of `domain -> address`, not browse/search UX by itself.
- Dynamic app subdomains may publish the currently controlling `address`, but they remain temporary leases and are cleaned up when the lease ends.

Success criteria:

- A relay root domain can be independently verified as currently controlled by one wallet `address`.
- ENS-aware clients can resolve `relay domain -> address` without trusting relay-local UI or API claims.
- Relay identity stays stable even if live ingress or overlay endpoints later change.

### Phase 3: Reward Engine v1

Objective:
Launch rewards without token and contract complexity.

Scope:

- Build an offchain reward index keyed by `Identity`, with rollups by owner `address`.
- Score relay operators and app publishers by selected signals:
  - uptime
  - successful traffic handling
  - app activity
  - referrals
  - public relay participation
- Add abuse controls:
  - self-traffic filtering
  - repeated low-value traffic suppression
  - operator/app collusion heuristics
  - minimum quality thresholds

Output:

- point balances
- epochs
- auditable reward reports

Success criteria:

- Rewards can run for multiple epochs without token issuance.
- Abuse patterns are measurable before any onchain commitment.

### Phase 4: Onchain Reward Claims on L2 Mainnet

Objective:
Turn rewards into Ethereum-native assets without moving dynamic scoring onchain.

Scope:

- Use `L2 mainnet` as the default settlement chain.
- Publish epoch results as Merkle roots or signed claim vouchers.
- Let users claim to their wallet address.
- Start with simple fungible rewards, then expand to reputation assets.

Recommended asset types:

- `ERC-20` for claimable rewards
- `ERC-1155` for seasonal badges or tiers
- non-transferable reputation badges later if needed

Success criteria:

- Users can claim rewards directly with the same address that owns their Portal identity.
- Reward distribution cost stays low enough for recurring epochs.

### Phase 5: x402 Paid Access

Objective:
Add payment-native access for HTTP workloads.

Scope:

- Support x402 where Portal has an HTTP handler boundary:
  - app-side x402 integration
  - tunnel-side `RunHTTP` / `--http-route` paywall middleware
  - paid relay-side HTTP services
- Use L2 mainnet and supported stable assets for default payment flows.
- Add facilitator-backed verification and settlement rather than requiring every seller to run chain infrastructure.

Non-goal:

- Do not attempt to force x402 across the full raw TLS passthrough path at relay edge.
- Raw passthrough traffic is not a global HTTP middleware boundary.

Success criteria:

- A Portal-published HTTP API can require payment without changing the relay transport model.
- Repeated access can later compose with wallet/session identity.

### Phase 6: ENS-Native App Naming

Objective:
Add ENS-native app identity only if temporary lease hostnames become insufficient.

Scope:

- Introduce `CCIP-Read`-backed resolution for dynamic names such as `app.relay.eth`.
- Keep actual service delivery on DNS + ACME hostnames.
- Use ENS names as an optional identity surface, with Portal providing the current target state.
- Map ENS app names to current Portal lease state and metadata only when persistent app identity is worth the added complexity.

Why later:

- Lease state is dynamic and short-lived.
- Today Portal app subdomains are intentionally temporary leases, not address-owned permanent namespaces.
- Dynamic app naming is a poor fit for direct onchain storage and ownership updates unless the product deliberately moves toward persistent app identities.

Success criteria:

- ENS names can represent live Portal apps without rewriting the transport plane.
- Dynamic service identity is possible without losing current HTTPS behavior.

### Phase 7: Relay Staking and Reputation

Objective:
Align public relay participation with durable Ethereum incentives.

Scope:

- Add optional staking for public relay operators.
- Add slashable rules for clear policy violations or persistent low-quality operation.
- Tie reward multipliers to quality and stake, not only raw traffic.
- Make operator reputation portable and wallet-bound.

Success criteria:

- Public relay operation has credible skin in the game.
- Rewards and visibility can favor reliable operators without central review of every action.

### Phase 8: Governance and Network Coordination

Objective:
Move from a product with crypto features to a real Ethereum-aligned network.

Scope:

- Formalize registry policy for public relays.
- Define reward allocation policy and eligibility rules.
- Introduce limited governance over network-level parameters only after incentives stabilize.

Non-goal:

- Do not put fast-moving runtime controls onchain too early.
- Governance should come after identity, discovery, and economics are already working.

Success criteria:

- Portal can coordinate a public relay ecosystem without abandoning product quality or operational simplicity.

## Recommended Execution Order

Near-term:

1. Phase 0
2. Phase 1
3. Phase 2

Mid-term:

4. Phase 3
5. Phase 4
6. Phase 5

Long-term:

7. Phase 6
8. Phase 7
9. Phase 8

## Preferred Chain Strategy

Default recommendation:

- `L2 mainnet` for rewards, x402 payments, and user-facing economic activity
- ENS as a hybrid model:
  - L1 anchor for resolution flow
  - L2-aware primary names for user identity
  - `CCIP-Read` for dynamic app naming

Reasoning:

- L2 economics fit frequent claims and low-value payments much better than L1.
- ENS identity can still remain canonical while user activity happens on L2.

## What This Roadmap Does Not Change

- Portal does not become an onchain transport protocol.
- Lease registration, renewal, and routing do not move to smart contracts.
- ACME is still required for public browser HTTPS.
- App subdomains do not become permanent address-owned namespaces by default.
- Ethereum integration should extend the product, not replace the networking model that already works.

## Summary

The intended end state is:

- Portal transport remains simple, fast, and mostly offchain.
- Portal `Identity` becomes the native product identity contract.
- Ethereum wallets become the canonical owners of those identities.
- ENS first becomes the verification layer for `domain -> address`, then later expands into richer naming and discovery only where it improves the product.
- L2 mainnet becomes the rewards and payments layer.
- x402 enables paid HTTP access where Portal has an HTTP boundary.

If executed in this order, Portal can become Ethereum-native without sacrificing its current simplicity or transport guarantees.

그 문구는 현재 코드 설명이라기보다, 가능한 다음 아키텍처를 설명한 말입니다.
지금 Portal에 그대로 적용된 상태는 아닙니다.

지금 구조를 코드로 보면 relay가 여전히 권한자입니다.

lease 등록을 relay가 승인하고 portal/api_server.go:534
hostname을 relay가 정하고 portal/api_server.go:542
hostname -> lease 매핑을 relay registry가 들고 있고 portal/lease.go:72
실제 ingress에서 SNI를 보고 어느 lease로 보낼지 relay가 결정합니다 portal/server.go:479
ENS gasless는 지금 여기에 검증 가능한 바깥 증거를 하나 더 붙인 겁니다.

등록 후 relay가 hostname -> address TXT를 publish함 portal/api_server.go:605
그래서 외부인은 “이 도메인이 현재 어느 address에 묶였는지”를 독립적으로 확인할 수 있음
즉 지금 달라진 것은:

relay가 말하는 owner를 외부에서 검증 가능
relay root와 app subdomain이 address-bound endpoint가 됨
지금 안 달라진 것은:

누가 붙는지
실제 어느 세션으로 라우팅할지
승인/차단 정책
이건 여전히 relay가 결정합니다.
그래서 그 문구의 뜻을 정확히 풀면 이겁니다.

현재 Portal:
relay가 identity + live mapping + routing을 다 가짐
ENS를 더 강하게 쓰는 미래 모델:
identity/ownership은 ENS나 서명된 state가 담당
relay는 live transport만 담당
client는 relay 주장 대신 ENS/서명된 state를 검증
하지만 중요한 보정이 있습니다.
현재 public HTTPS ingress 모델에서는 relay를 완전 dumb pipe로 만들 수는 없습니다.
relay는 여전히:

active lease가 살아 있는지
ready reverse session이 있는지
어느 연결로 브리지할지
를 알아야 하기 때문입니다.
즉 그 문구를 Portal 현실에 맞게 바꾸면:

완전 authority 제거는 아님
ownership/identity에 대한 authority를 relay 밖으로 뺄 수 있음
live session routing authority는 아직 relay 안에 남음
한 줄로 줄이면:
지금 ENS가 만든 변화는 “relay를 안 믿고도 owner identity를 검증할 수 있게 된 것”이고, 아직 “relay가 routing 권한을 잃은 것”은 아닙니다.

원하면 다음 답변에서

현재 구조
ENS 강화 구조
컨트랙트 source-of-truth 구조
를 3단 비교로 그려드리겠습니다.

그건 지금 단계에서 붙여도 사용성에 변화가 없어?


거의 없습니다. 지금 단계에서 붙여도 내부 설계 의미는 있지만, 사용자 체감 사용성 변화는 작습니다.

이유는 간단합니다.
지금 사용자 플로우는 여전히:

relay에 등록
relay가 hostname 할당
relay가 route 유지
사용자는 그 URL로 접속
이 구조라서, ENS가 있어도 접속 방식 자체는 안 바뀝니다.

지금 바로 체감되는 변화는 이 정도뿐입니다.

“이 도메인이 현재 어느 address에 묶였는지” 검증 가능
relay/operator/app identity를 address 기준으로 설명 가능
외부에서 더 신뢰성 있게 표시 가능
하지만 대부분의 일반 사용자는:

ENS-aware client를 쓰지 않으면 못 느낌
접속 URL도 그대로
publish 방식도 그대로
relay 신뢰 모델도 대부분 그대로
즉 지금 붙이면 생기는 건 주로:

검증성
설명 가능성
미래 확장성
이지,
즉시 UX 개선
은 아닙니다.
사용성 변화가 커지려면 ENS가 아래 중 하나와 연결돼야 합니다.

UI에서 verified owner/ENS name 표시
catalog/search에서 owner/address 기준 묶기
결제/정산에서 payee identity 표시
relay 선택/평판 시스템과 연결
persistent name claim
그래서 냉정하게 말하면:

backend에만 붙이면 체감 변화 작음
UI/selection/payment와 연결되면 의미 커짐
한 줄로 줄이면:
지금 단계에서 ENS를 더 붙여도 “맞는 방향”일 수는 있지만, 그 자체만으로 사용성이 크게 달라지진 않습니다.