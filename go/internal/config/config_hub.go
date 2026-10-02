// Package config - Hub (multi-agent orchestrator) configuration.
//
// The Neural Hive Hub is the central orchestration layer that sits between the existing backend
// and a pool of independent, capability-tagged model-backed agent instances. This file resolves
// the Hub settings (listen port, HIVE policy, aggregator model, state file) and the agent roster
// (how many DeepSeek agents to run, on which ports and with which capability tags) from the
// environment / .env, so the pool size and its capabilities are configuration, never code.
package config

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Hub is the resolved configuration of the Neural Hive Hub and its agent pool.
type Hub struct {
	Port             int
	AgentCount       int
	AgentBasePort    int
	AgentStartIndex  int
	AgentModel       string
	HIVE             HIVEPolicy
	AggregatorModel  string
	AggregatorSystem string
	StateFile        string
	CoordinatorURL   string
	Roster           []HubAgent

	// ---- HIVE payment (MetaMask) ----

	// PaymentRequired makes POST /task demand a verified on-chain HIVE payment (HUB_REQUIRE_PAYMENT).
	PaymentRequired bool
	// PricePerAgent is the configured price of one agent in HIVE, as a decimal string (HIVE_PRICE_PER_AGENT).
	PricePerAgent string
	// PricePerAgentWei is PricePerAgent in the token's 18-decimal base unit.
	PricePerAgentWei *big.Int
	// Treasury receives the HIVE paid for tasks (HIVE_TREASURY_ADDRESS, default ADDRESS_1).
	Treasury string
	// TokenAddress is the deployed HiveToken the payment must be made in.
	TokenAddress string
	// RPCURL / ChainID / Network describe the chain MetaMask must be on and the Hub verifies against.
	RPCURL  string
	ChainID int64
	Network string
	// QuoteTTL is how long a price quote stays valid.
	QuoteTTL time.Duration
	// HistoryLimit is how many prior chat turns are forwarded to the agents (HUB_HISTORY_LIMIT).
	HistoryLimit int

	// HubFeePercent is the share of each task payment the Hub keeps (HUB_FEE_PERCENT, default 2).
	// The remainder is split equally among the agents that served the task. Set 0 for no fee.
	HubFeePercent float64

	// HubPrivateKey signs the on-chain settlement transfers and the agent-owner operations
	// (HUB_PRIVATE_KEY, default PRIVATE_KEY_1, which is the deployer that owns the seed agents).
	HubPrivateKey string
	// AgentKeySeed derives each agent payout wallet deterministically (AGENT_KEY_SEED).
	AgentKeySeed string

	// SelfTestToken lets start.ps1 run its own verification without MetaMask (HUB_SELFTEST_TOKEN).
	// It is generated per start and never stored in .env.
	// RegistryAddress is the deployed CapabilityRegistry agents register in on-chain.
	RegistryAddress string
	// WalletFactoryAddress is the deployed AgentWalletFactory that deploys one AgentWallet
	// smart contract per agent (AGENT_WALLET_FACTORY_ADDRESS / deployments.json).
	WalletFactoryAddress string

	// RegistrationFee is the HIVE a wallet must pay to register an agent (HIVE_REGISTRATION_FEE, default 5).
	RegistrationFee string
	// RegistrationFeeWei is RegistrationFee in the token 18-decimal base unit.
	RegistrationFeeWei *big.Int
	SelfTestToken   string
}

// parseHiveAmount converts a decimal HIVE amount ("1", "0.5", "2.25") to 18-decimal base units.
func parseHiveAmount(s string) (*big.Int, bool) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
	if !ok || r.Sign() < 0 {
		return nil, false
	}
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	r.Mul(r, new(big.Rat).SetInt(unit))
	return new(big.Int).Quo(r.Num(), r.Denom()), true
}

func getBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(get(key, "")))
	switch v {
	case "":
		return def
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// HIVEPolicy is the agent-count policy of the Hub: how many independent agents a task needs.
// A simple task needs HIVE.Default (1); a complex task needs HIVE.Complex; HIVE.Max is the
// hard ceiling and the pool size is the effective limit.
type HIVEPolicy struct {
	Default int
	Complex int
	Max     int
}

// HubAgent is one configured agent instance in the pool.
type HubAgent struct {
	ID    string
	Index int
	Port  int
	Tags  []string
	Model string
}

// Endpoint returns the loopback URL of the agent instance.
func (a HubAgent) Endpoint() string { return fmt.Sprintf("http://127.0.0.1:%d", a.Port) }

// defaultTagTable is the built-in capability rotation used when no roster/tags are configured.
// Every entry becomes one independent DeepSeek worker with its own tags and identity.
var defaultTagTable = [][]string{
	{"research"},
	{"analysis", "research"},
	{"coding"},
	{"security", "blockchain"},
	{"planning", "reasoning"},
	{"validation", "reasoning"},
	{"blockchain", "security"},
	{"architecture", "planning"},
	{"summarization", "research"},
	{"data", "analysis"},
}

const defaultAggregatorSystem = "You are the Neural Hive Hub aggregation layer. You receive an " +
	"original task and the answers produced by several independent specialist agents, each tagged " +
	"with the capability it was selected for. Synthesise them into one coherent, accurate final " +
	"answer for the user: resolve overlaps, keep concrete specifics, note genuine disagreements, " +
	"and never invent facts the agents did not provide."

// loadHub resolves the Hub configuration and the agent roster.
func loadHub() Hub {
	defaultModel := get("DEEPSEEK_MODEL", "deepseek-chat")
	h := Hub{
		Port:             getInt("HUB_PORT", 9500),
		AgentCount:       getInt("HIVE_AGENT_COUNT", 5),
		AgentBasePort:    getInt("HIVE_AGENT_BASE_PORT", 9401),
		AgentStartIndex:  getInt("HIVE_AGENT_START_INDEX", 100),
		AgentModel:       get("HUB_AGENT_MODEL", defaultModel),
		AggregatorModel:  get("HUB_AGGREGATOR_MODEL", defaultModel),
		AggregatorSystem: get("HUB_AGGREGATOR_SYSTEM", defaultAggregatorSystem),
		StateFile:        get("HUB_STATE_FILE", "logs/hub-state.json"),
		CoordinatorURL:   get("COORDINATOR_API_URL", "http://127.0.0.1:9200"),
		HIVE: HIVEPolicy{
			Default: getInt("HIVE_DEFAULT", 1),
			Complex: getInt("HIVE_COMPLEX", 2),
			Max:     getInt("HIVE_MAX", 10),
		},
	}
	if h.AgentCount < 1 {
		h.AgentCount = 1
	}
	if h.HIVE.Default < 1 {
		h.HIVE.Default = 1
	}
	if h.HIVE.Complex < h.HIVE.Default {
		h.HIVE.Complex = h.HIVE.Default
	}
	if h.HIVE.Max < h.HIVE.Complex {
		h.HIVE.Max = h.HIVE.Complex
	}
	h.Roster = loadRoster(h)

	h.PaymentRequired = getBool("HUB_REQUIRE_PAYMENT", true)
	h.PricePerAgent = strings.TrimSpace(get("HIVE_PRICE_PER_AGENT", "1"))
	wei, ok := parseHiveAmount(h.PricePerAgent)
	if !ok {
		h.PricePerAgent = "1"
		wei, _ = parseHiveAmount("1")
	}
	h.PricePerAgentWei = wei
	h.Treasury = strings.TrimSpace(get("HIVE_TREASURY_ADDRESS", ""))
	h.QuoteTTL = time.Duration(getInt("HIVE_QUOTE_TTL_SECONDS", 900)) * time.Second
	h.SelfTestToken = strings.TrimSpace(get("HUB_SELFTEST_TOKEN", ""))
	h.HistoryLimit = getInt("HUB_HISTORY_LIMIT", 8)
	h.HubFeePercent = getFloat("HUB_FEE_PERCENT", 2)
	h.HubPrivateKey = strings.TrimSpace(get("HUB_PRIVATE_KEY", ""))
	h.AgentKeySeed = strings.TrimSpace(get("AGENT_KEY_SEED", "neural-hive-devnet"))
	h.RegistrationFee = strings.TrimSpace(get("HIVE_REGISTRATION_FEE", "5"))
	rwei, rok := parseHiveAmount(h.RegistrationFee)
	if !rok {
		h.RegistrationFee = "5"
		rwei, _ = parseHiveAmount("5")
	}
	h.RegistrationFeeWei = rwei
	return h
}

// loadRoster builds the ordered agent roster. Precedence:
//  1. HIVE_AGENT_ROSTER - explicit "id:port:tag,tag;id:port:tag,tag" header (full control)
//  2. AGENT_<n>_TAGS    - per-instance tag override on top of the built-in rotation
//  3. built-in rotation - one entry per configured agent
func loadRoster(h Hub) []HubAgent {
	if raw := strings.TrimSpace(get("HIVE_AGENT_ROSTER", "")); raw != "" {
		if roster := parseRoster(raw, h); len(roster) > 0 {
			return roster
		}
	}
	out := make([]HubAgent, 0, h.AgentCount)
	for i := 0; i < h.AgentCount; i++ {
		tags := defaultTagTable[i%len(defaultTagTable)]
		if override := strings.TrimSpace(get(fmt.Sprintf("AGENT_%d_TAGS", i+1), "")); override != "" {
			tags = splitTrim(override)
		}
		out = append(out, HubAgent{
			ID:    fmt.Sprintf("agent-%02d", i+1),
			Index: h.AgentStartIndex + i,
			Port:  h.AgentBasePort + i,
			Tags:  append([]string{}, tags...),
			Model: h.AgentModel,
		})
	}
	return out
}

// parseRoster parses "id:port:tags;id:port:tags"; a bare "id:port" keeps the built-in tags.
func parseRoster(raw string, h Hub) []HubAgent {
	out := []HubAgent{}
	for i, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) < 2 {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || port <= 0 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		if id == "" {
			id = fmt.Sprintf("agent-%02d", i+1)
		}
		tags := defaultTagTable[i%len(defaultTagTable)]
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			tags = splitTrim(parts[2])
		}
		out = append(out, HubAgent{
			ID:    id,
			Index: h.AgentStartIndex + i,
			Port:  port,
			Tags:  append([]string{}, tags...),
			Model: h.AgentModel,
		})
	}
	return out
}

func splitTrim(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
