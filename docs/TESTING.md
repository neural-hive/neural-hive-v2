# Testing Neural Hive

All tests are run by `start.ps1`, which reports PASS or FAIL for every stage. Nothing here has to be run by hand, but each command below can be run from the repository root.

## 1. Contract tests (Truffle, `contracts/test`)

Run against the already-running Ganache:

```
cd contracts
npx truffle test --network ganache
```

| File | Coverage |
| --- | --- |
| `01_registries.test.js` | `HiveToken` supply, minter role and cap; `CapabilityRegistry` registration, duplicate and signer reuse rejection, EIP-712 operator/signer binding, price cap and deadline, capability/price/endpoint/active updates, signer rotation, outcome role; `ReputationRegistry` trust signals, optimistic batch finalisation, neutral prior, challenged batch veto, role enforcement. |
| `02_settlement.test.js` | staking, zero-stake rejection, lock and unlock, unbonding cooldown, dust-stake rejection, escrow and settlement with a protocol fee, slashing capped at available stake, admin parameters and authorisation. |
| `03_coordinator.test.js` | full request lifecycle (escrow, assign, signed responses, on-chain Krum, VCG payout, refund), one lying agent in a five-agent committee, rejection of a response signed by the wrong key, one response per agent plus per-agent nonce uniqueness, administration and assignment eligibility, plurality over non-numeric outputs, step failure when the committee under-answers, and the ICM delivery and answer path. |
| `04_snowball.test.js` | overturning a wrong consensus and slashing the liar, rejecting a baseless challenge and forfeiting the challenger stake, and rejecting disputes from unregistered challengers and verifiers. |

`contracts/test/helpers.js` builds the EIP-712 digests, signs attestations with `ethereumjs-util`, and creates fresh devnet keys. Fresh keys are added to the in-memory `web3.eth.accounts.wallet` so transactions are signed locally and do not depend on the node exposing an unlocked-account API.

## 2. Go unit tests

```
cd go
go test ./...
```

| Package | Coverage |
| --- | --- |
| `internal/capability` | deterministic and normalised skill embeddings, cosine bounds, fixed-point on-chain round trip. |
| `internal/hnsw` | insertion, nearest-neighbour ordering, replacement, soft delete and empty-index behaviour. |
| `internal/routing` | gate weighting, candidate ranking and truncation, selfish argmin and tie-breaks, the price-of-anarchy watchdog switching modes, and LinUCB exploration and learning. |
| `internal/consensus` | Krum outlier isolation and fault bounds, Krum scores and plurality, EigenTrust row normalisation and trust propagation, Snowball convergence and the round limit. |
| `internal/auction` | VCG winner, second-price payment, deterministic tie-break and eligibility filtering. |
| `internal/decompose` | request decomposition, dependency order, critical path, allocation priorities, cycle detection and the single-step fallback. |
| `internal/crypto` | EIP-712 response sign/recover, domain separation by chain and contract, malformed signatures, output hashing and registration binding. |
| `internal/agent` | deterministic key derivation, canonical answers, the byzantine shift, verifiable signed responses and the mode override and refusal path. |
| `internal/config` | `.env` parsing that never overwrites the environment, integer fallback, and deployment-address overrides including the throwaway `test` network guard. |

## 3. End-to-end protocol run

```
cd go
go run ./cmd/e2e
```

It requires the agent services and the relayer to be up (which `start.ps1` arranges) and drives nine scenarios, each printing PASS or FAIL:

1. happy path: decompose, route, sign, on-chain Krum, VCG settle, escrow refund;
2. byzantine committee: a lying agent must not skew the consensus and must not be paid;
3. forged signature rejected, valid signature accepted;
4. replay rejected and the per-agent nonce consumed;
5. unauthorised assignment rejected, mismatched key binding rejected, price cap enforced;
6. Hive Snowball dispute upheld, liar slashed, step invalidated, escrow returned;
7. EigenTrust computed off-chain and applied through the optimistic on-chain batch;
8. ICM request created from another chain and answered back without a bridge;
9. bandit fallback routing and the price-of-anarchy watchdog.

The run also measures the cache-hit routing path over 300 calls and checks it against the proposal KPI of p95 under 300 ms.

## 4. Automation

`start.ps1` chains all of the above and prints a stage-by-stage summary. It is the only command you need.


## 5. Neural Hive Hub (multi-agent DeepSeek)

Unit and integration tests need no live API: a fake `llm.Provider` and `httptest` agent servers
are injected.

```
cd go
go test ./internal/hub/...
```

| Area | Coverage |
| --- | --- |
| classify | simple vs complex detection and its explainable signals |
| decompose | exactly HIVE capability-tagged subtasks |
| HIVE policy | simple = 1; complex grows toward the objective count |
| selection | tag-match preference, unhealthy agents skipped, in-use agents avoided |
| registry | health, online count, sorted snapshot, reputation/risk update, persistence round trip |
| execution | simple run (one agent), complex run (distinct agents), partial failure, per-call timeout |
| aggregation | single answer returned directly; labelled fallback when the aggregator is unavailable |
| deepseek client | request construction and auth header, model selection, 401/429/5xx mapping, malformed and empty responses, timeout, and that the API key never leaks in an error |

End-to-end scenarios are run by the user through `start.ps1` (never during implementation). See
`docs/HUB.md` for the exact commands: a simple question expects HIVE = 1 and one DeepSeek agent; a
complex question expects HIVE >= 2, several tag-routed agents, concurrent real answers, one
aggregated final answer, a full execution trace and hash/blockchain evidence.
