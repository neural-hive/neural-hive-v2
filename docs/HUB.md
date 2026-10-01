# Neural Hive Hub - multi-agent DeepSeek orchestrator

This document describes the multi-agent DeepSeek layer that sits between the existing backend and
the browser, and how to run the two end-to-end scenarios.

## Request path

```
Browser (web/index.html)  ->  Hub (:9500)  ->  DeepSeek agent pool (:9401..)
                                                        |
                                                   DeepSeek API
                                                        |
    Hub aggregation  <-  independent agent answers  <---+
         |
    final answer + execution trace + answer hash  ->  Browser
```

The existing coordinator (:9200) is reused only to anchor the final answer hash on-chain; it is
never in the answer path. The existing mock on-chain agents (:9101-9105) are untouched and can
run alongside the DeepSeek pool.

## Configuration (.env)

The DeepSeek key is server-side only: it is read by the Go agents, placed only in the outgoing
Authorization header, and never logged, returned or exposed to the browser.

| Variable | Meaning | Default |
| --- | --- | --- |
| DEEPSEEK_API_KEY | DeepSeek API key (secret) | (required) |
| DEEPSEEK_BASE_URL | DeepSeek endpoint | https://api.deepseek.com |
| DEEPSEEK_MODEL | chat model | deepseek-chat |
| DEEPSEEK_TIMEOUT_SECONDS | per-completion timeout | 120 |
| HUB_PORT | Hub listen port | 9500 |
| HIVE_AGENT_COUNT | number of DeepSeek agents (1..10) | 5 |
| HIVE_AGENT_BASE_PORT | first agent port | 9401 |
| HIVE_AGENT_START_INDEX | first identity index | 100 |
| AGENT_<n>_TAGS | capability tags of instance n | built-in rotation |
| HIVE_AGENT_ROSTER | explicit id:port:tags;... override | (none) |
| HIVE_DEFAULT / HIVE_COMPLEX / HIVE_MAX | agents per simple / complex task, ceiling | 1 / 2 / 10 |
| HUB_STATE_FILE | persistence of reputation/risk | logs/hub-state.json |

> The DeepSeek pool listens on 9401+ by default so it never collides with the preserved mock
> on-chain agents on 9101-9105 and the deepseek-agent on 9107. Ports and tags are configuration.

## Agent pool

Every instance is the same implementation (cmd/hive-agents launches one deepworker.Server per
roster entry). Default 5-agent rotation:

| Agent | Port | Tags |
| --- | --- | --- |
| agent-01 | 9401 | research |
| agent-02 | 9402 | analysis, research |
| agent-03 | 9403 | coding |
| agent-04 | 9404 | security, blockchain |
| agent-05 | 9405 | planning, reasoning |

Set HIVE_AGENT_COUNT up to 10 to extend the roster (agent-06..agent-10, ports 9406..9410).

## Hub behaviour

1. **Classify** (internal/hub/classify.go): a deterministic, explainable heuristic scores objective
   verbs, clause count and domain count; score >= 3 is complex.
2. **HIVE**: simple -> HIVE_DEFAULT (1); complex -> HIVE_COMPLEX (2), grown toward the number of
   objectives, capped by HIVE_MAX and by the number of online agents.
3. **Decompose** (internal/hub/classify.go): the task is split into exactly HIVE subtasks, each with
   the capability tags it requires (from the task objectives and detected domains).
4. **Select** (internal/hub/orchestrate.go): eligible online agents are ranked by
   tag overlap (0.45), reputation (0.20), inverted risk (0.15), historical success (0.10) and
   latency (0.10). A tag match is preferred and an agent already used on this task is avoided, so
   independent subtasks run on different agents. Nothing is random.
5. **Execute concurrently**: each subtask is dispatched to its agent over HTTP in its own
   goroutine, with a per-call context timeout. One failed agent never blocks the others.
6. **Aggregate**: the successful answers are synthesised into one final answer by the shared
   DeepSeek client (internal/llm). A single answer is returned directly; if the aggregator is
   unavailable the real answers are shown verbatim, clearly labelled - never invented.
7. **Hash + anchor**: the final answer hash is computed (Keccak-256) and anchored through the
   existing backend (POST COORDINATOR_API_URL/anchor).

## Reputation and risk

Both start neutral and move only from observed outcomes (internal/hub/registry.go):

```
success: r <- r + 0.15*(1-r)      failure: r <- r - 0.15*r
success: k <- k - 0.20*k          failure: k <- k + 0.20*(1-k)
```

with r, k in [0,1], initial reputation 0.5 and initial risk 0.1. Successes, failures, timeouts,
executions and average latency are tracked per agent and persisted to HUB_STATE_FILE.

## Hub HTTP API

| Method | Path | Purpose |
| --- | --- | --- |
| POST | /task | submit {task or request, hive?, quoteId, txHash}; verifies the HIVE payment, returns the full Result (incl. `payment`) |
| GET | /agents | registry cards (tags, status, reputation, risk, stats) |
| GET | /agents/{id} | one agent card |
| GET | /trace/{id} | full execution trace for one task id |
| GET | /history | recent tasks (newest first) |
| GET | /network | pool summary (online, latency, HIVE policy, provider) |
| GET | /health | liveness |
| GET | /payment/config | price per agent, HiveToken, treasury, chain |
| POST | /quote | price a task in HIVE (agents x price) before paying |
| POST | /faucet | devnet only: top a wallet up with test HIVE and gas |
| GET | / | the agentic workspace UI |

## Paying for a task in HIVE (MetaMask)

Every request is paid in HIVE, the project's ERC-20, from the user's MetaMask wallet.

```
cost (HIVE) = agents required (the task's HIVE number) x HIVE_PRICE_PER_AGENT
```

A simple request needs 1 agent, so with the default price of 1 HIVE per agent it costs 1 HIVE; a
complex request that needs 3 agents costs 3 HIVE.

Flow when the user presses **Send** in the workspace:

1. The UI connects MetaMask (`eth_requestAccounts`) and switches to / adds the devnet (chain id 1337).
2. `POST /quote` classifies the task and returns the agent count, the cost, the HiveToken address and
   the treasury address, plus a one-time quote id.
3. On the local devnet, if the wallet holds too little HIVE or gas, the UI calls `POST /faucet`
   (served by the backend coordinator, which owns the admin key) to top it up with test funds.
4. MetaMask shows a HiveToken `transfer(treasury, cost)`; the user confirms it.
5. The UI waits for the receipt, then calls `POST /task` with `{task, quoteId, txHash}`.
6. The Hub reads the receipt from the chain and runs the agents only if the HiveToken emitted a
   `Transfer` of at least the quoted cost to the treasury. Each quote and each transaction hash can
   be used once (spent hashes are kept in `logs/hub-payments.jsonl`, so a restart cannot replay one).
7. The answer comes back with a `payment` object (payer, tx hash, block, agents, price per agent, cost,
   amount paid). The UI shows it under every answer and in **Show execution details**.

No smart-contract change is needed: the payment is a plain HiveToken transfer, verified from its
`Transfer` event. The Hub never holds a user key.

| Variable | Meaning | Default |
| --- | --- | --- |
| HUB_REQUIRE_PAYMENT | demand a verified HIVE payment on `POST /task` | true |
| HIVE_PRICE_PER_AGENT | price of one agent, in HIVE (decimals allowed) | 1 |
| HIVE_TREASURY_ADDRESS | account that receives the HIVE | ADDRESS_1 |
| HIVE_FAUCET_AMOUNT | devnet faucet tops a wallet up to this much HIVE | 100 |
| HIVE_QUOTE_TTL_SECONDS | how long a quote stays valid | 900 |

Extra endpoints: `GET /payment/config`, `POST /quote`, `POST /faucet` (ganache only). `start.ps1`
verifies its own end-to-end run through a per-start secret header (`X-Hive-Selftest`, generated in
memory, never stored in `.env`), and also checks that an unpaid `POST /task` is refused with HTTP 402.

MetaMask setup for the local devnet: network RPC `http://127.0.0.1:8545`, chain id `1337`, currency
ETH (the UI adds this network for you). If you restart Ganache, use MetaMask Settings > Advanced >
Clear activity tab data, otherwise MetaMask keeps the old account nonce.

## End-to-end procedure

Start Ganache, then run the repository entry point (this launches the pool, the Hub, the backend
and serves the frontend):

```
ganache --chain.chainId 1337 --wallet.deterministic --server.port 8545
powershell -ExecutionPolicy Bypass -File .\\start.ps1
```

Open the workspace at http://127.0.0.1:9500/ .

### Scenario 1 - simple

Question: `What is distributed consensus?`

Expected: complexity simple, HIVE = 1, exactly one DeepSeek agent, a real DeepSeek answer, an
answer hash and (if the backend is up) an anchor transaction. Verify with:

```
$b = @{ task = "What is distributed consensus?" } | ConvertTo-Json
Invoke-RestMethod -Uri http://127.0.0.1:9500/task -Method Post -ContentType "application/json" -Body $b | ConvertTo-Json -Depth 6
```

### Scenario 2 - complex

Question: `Analyze the architecture of a decentralized AI agent network, identify security risks, and propose an implementation strategy.`

Expected: complexity complex, HIVE >= 2, at least two subtasks routed to different agents by
tag, concurrent real DeepSeek answers, one aggregated final answer, a full execution trace and
hash/blockchain evidence. Verify with:

```
$b = @{ task = "Analyze the architecture of a decentralized AI agent network, identify security risks, and propose an implementation strategy." } | ConvertTo-Json
Invoke-RestMethod -Uri http://127.0.0.1:9500/task -Method Post -ContentType "application/json" -Body $b | ConvertTo-Json -Depth 6
```

In the UI, expand **Show execution details** on the answer to see the task, the participating
agents and ports, per-subtask routing and reputation/risk movement, the aggregation step and the
hash/blockchain evidence. Only agents that actually ran appear.

> These scenarios are run by start.ps1 / the user. They are not executed during implementation.

## Logs

| File | Written by |
| --- | --- |
| logs/implementation.log | start.ps1 (startup banner) |
| logs/hub.log | Hub (stdout) |
| logs/agents.log | hive-agents / hive-agent (stdout) |
| logs/backend.log | coordinator (stdout) |
| logs/frontend.log | Hub UI access log |
| logs/e2e.log | start.ps1 end-to-end run |
| logs/errors.log | start.ps1 aggregation of *.err.log |

The DeepSeek API key is never written to any log.
