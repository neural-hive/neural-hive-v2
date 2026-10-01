# Neural Hive architecture

## Components

### On-chain (Solidity, `contracts/contracts`)

| Contract | Responsibility |
| --- | --- |
| `HiveToken` | Capped-supply ERC-20. Minting is gated by `MINTER_ROLE`; slashed stake is burned. |
| `CapabilityRegistry` | Source of truth for agent identity: operator, response signing key, capability vector `int256[8]`, endpoint, price, minimum stake, outcome counters. Supports key rotation with a fresh EIP-712 binding signature. |
| `ReputationRegistry` | Stores EigenTrust scores, accepts signed trust signals, and applies a proposed score vector as an optimistic batch that only becomes canonical once a challenge window elapses with no successful challenge. |
| `StakingSettlement` | Custody of agent stake and per-request escrow. Locks stake for the duration of an assignment, pays a winner and the treasury, refunds unused escrow, and slashes an agent proven wrong. |
| `TaskCoordinator` | Request lifecycle: escrow, step registration, agent assignment, signed response collection, on-chain Krum aggregation, VCG payout, dispute hooks and the ICM entry and exit points. |
| `HiveSnowball` | Dispute resolution modelled on Avalanche consensus: a random verifier sample is polled repeatedly and a preference finalises at a confidence threshold. Can slash and invalidate a step. |
| `MockTeleporterMessenger` | Local stand-in for Avalanche Interchain Messaging on the devnet. |
| `libraries/AttestationLib.sol` | EIP-712 digests and signature recovery for agent responses and operator/signer registration. |
| `libraries/Krum.sol` | The Krum rule, executed on-chain over the collected signed responses. |

### Off-chain (Go, `go/`)

| Package | Responsibility |
| --- | --- |
| `internal/hnsw` | Approximate nearest-neighbour index over capability vectors. |
| `internal/capability` | Deterministic skill embeddings and distance metrics. |
| `internal/routing` | The learned gate, selfish routing with the price-of-anarchy watchdog, and the LinUCB fallback. |
| `internal/decompose` | Request decomposition, topological sort and critical path scheduling. |
| `internal/consensus` | Reference Krum, EigenTrust and Snowball implementations. |
| `internal/auction` | VCG allocation and payment. |
| `internal/crypto` | EIP-712 attestation digests, signing and recovery. |
| `internal/chain` | ABI loading from the Truffle artifacts and typed transaction helpers. |
| `internal/coordinator` | Request orchestration, agent bootstrap and the relayer wire format. |
| `internal/agent` | The worker agent: canonical compute plus signed responses. |
| `cmd/*` | The four services: `agent`, `coordinator`, `relayer`, `e2e`. |

## Trust model

- Agents are untrusted. Each binds a signing key on-chain at registration; the coordinator accepts a response only if the signature recovers to the bound key. Nonces and deadlines give replay protection.
- The coordinator is untrusted for correctness. It may choose assignments, but the contracts recompute Krum over the signed responses and recompute the payout, so it cannot substitute a different consensus or a different payment.
- The relayer is permissionless and untrusted. It never sees an agent private key, so it cannot forge an answer; it can at worst censor, which the coordinator can route around by submitting directly.
- Reputation updates are optimistic: a proposed EigenTrust vector is open to challenge for `challengeWindow` seconds and Hive Snowball arbitrates disputes.
- Stake is the Sybil cost: manufacturing reputation with colluding accounts is bounded by the stake that can be slashed.

## Request lifecycle

1. A requester (or an external L1 over ICM) creates a request and escrows a HIVE budget.
2. The coordinator decomposes the request into a step graph and schedules it by critical path.
3. For each step, candidates are retrieved from the HNSW index and re-ranked by the learned gate, then selfish routing (or LinUCB once the watchdog trips) selects the committee.
4. Each assigned agent computes an answer and returns an EIP-712 signed attestation, delivered directly or through the relayer.
5. `finalizeStep` recomputes Krum over the signed responses and fixes the consensus and the VCG-cleared payout.
6. `settleRequest` pays each step winner, pays the protocol fee to the treasury, records outcomes and refunds unused escrow.
7. A challenger can contest a finalised step through Hive Snowball; if the challenge is upheld the agent is slashed and the step invalidated.

## Interchain messaging

`TaskCoordinator` implements `ITeleporterReceiver`. An inbound ICM message is ABI-encoded as `(bytes32 metaHash, uint256 budget, uint8 tier, address payer)` and creates a request whose payer pre-approved `StakingSettlement`. The aggregated answer hash is sent back with `sendInterchainAnswer` through `ITeleporterMessenger`. No bridge is involved.

