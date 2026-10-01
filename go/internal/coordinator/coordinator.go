// Package coordinator is the off-chain coordination layer of Neural Hive.
//
// It owns everything the proposal puts off-chain: the HNSW capability index (4.2.1), selfish and
// bandit routing (4.2.2, 4.2.3), request decomposition and critical-path scheduling (4.2.4),
// Krum/EigenTrust/Snowball reference implementations (4.2.5, 4.2.6, 4.2.8), VCG bookkeeping
// (4.2.7) and the relayer that carries signed agent responses on-chain.
//
// It is explicitly NOT trusted with funds or correctness: it can choose who works, but the
// contracts re-verify every signature, recompute Krum, recompute the VCG payout and will slash
// through Hive Snowball if the coordinator routes around the truth.
package coordinator

import (
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/neural-hive/hive-node/internal/agent"
	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/chain"
	"github.com/neural-hive/hive-node/internal/config"
	"github.com/neural-hive/hive-node/internal/hnsw"
	"github.com/neural-hive/hive-node/internal/routing"
)

// Coordinator holds the live protocol state view plus routing machinery.
type Coordinator struct {
	Cfg      *config.Config
	Admin    *chain.Client
	Agents   []*agent.Agent
	ClientOf map[string]*chain.Client
	Index    *hnsw.Index
	Views    map[string]routing.AgentView
	Selfish  *routing.SelfishRouter
	Bandit   *routing.LinUCB
	Seed     string
	mu       sync.Mutex
}

// AgentSpec describes an agent to bootstrap.
type AgentSpec struct {
	Index    int
	Skills   []string
	Price    *big.Int // HIVE wei per task
	Endpoint string
	Mode     agent.Mode
}

// NewCoordinator builds a coordinator around an admin client.
func NewCoordinator(cfg *config.Config, admin *chain.Client, seed string) *Coordinator {
	return &Coordinator{
		Cfg:      cfg,
		Admin:    admin,
		ClientOf: map[string]*chain.Client{},
		Views:    map[string]routing.AgentView{},
		Index:    hnsw.New(8, 32, 32, capability.Dim, 42),
		Selfish:  routing.NewSelfishRouter(1.5),
		Bandit:   routing.NewLinUCB(capability.Dim, 0.4),
		Seed:     seed,
	}
}

// DefaultAgentSpecs is the seed population: a small set of open agents with distinct skills,
// which is what Phase 1 of the roadmap calls for.
func DefaultAgentSpecs(baseURLs []string, price *big.Int) []AgentSpec {
	skills := [][]string{
		{"risk-scoring", "anomaly-detection"},
		{"explain", "summarization"},
		{"compliance", "moderation"},
		{"market-data", "trading-signal"},
		{"smart-contract-audit", "code-review"},
	}
	specs := make([]AgentSpec, 0, len(skills))
	for i, s := range skills {
		ep := ""
		if i < len(baseURLs) {
			ep = baseURLs[i]
		}
		p := new(big.Int).Set(price)
		p.Add(p, big.NewInt(int64(i)*1e15))
		specs = append(specs, AgentSpec{Index: i, Skills: s, Price: p, Endpoint: ep, Mode: agent.ModeHonest})
	}
	return specs
}

// NewClient builds an authenticated client for a raw private key.
func NewClient(cfg *config.Config, keyHex string) (*chain.Client, error) {
	return chain.New(cfg, keyHex)
}

// AgentAddress derives the deterministic devnet address for an agent index.
func AgentAddress(seed string, i int) (common.Address, error) {
	k, err := agent.DeriveKey(seed, i)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(k.PublicKey), nil
}

// OllamaAgentSpec describes the model-backed answer agent. It registers with the same on-chain
// identity mechanism as the mock agents (a distinct index, so a distinct derived key) and
// advertises the skills the conversational workspace routes to.
func OllamaAgentSpec(index int, endpoint string, price *big.Int) AgentSpec {
	return AgentSpec{
		Index:    index,
		Skills:   []string{"explain", "summarization", "reasoning"},
		Price:    price,
		Endpoint: endpoint,
		Mode:     agent.ModeHonest,
	}
}

// DeepSeekAgentSpec describes the real DeepSeek-backed answer agent. It registers with the same
// on-chain identity mechanism as the mock agents (a distinct index, so a distinct derived key)
// and advertises the skills the conversational workspace routes to. It is the designated
// natural-language answerer on the final step of a request.
func DeepSeekAgentSpec(index int, endpoint string, price *big.Int) AgentSpec {
	return AgentSpec{
		Index:    index,
		Skills:   []string{"explain", "summarization", "reasoning"},
		Price:    price,
		Endpoint: endpoint,
		Mode:     agent.ModeHonest,
	}
}
