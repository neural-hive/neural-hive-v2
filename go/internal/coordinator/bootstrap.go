package coordinator

import (
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/neural-hive/hive-node/internal/agent"
	"github.com/neural-hive/hive-node/internal/capability"
	"github.com/neural-hive/hive-node/internal/chain"
	"github.com/neural-hive/hive-node/internal/hnsw"
	"github.com/neural-hive/hive-node/internal/routing"
)

var (
	ethFundPerAgent  = new(big.Int).Mul(big.NewInt(2), big.NewInt(1e18))
	hiveFundPerAgent = new(big.Int).Mul(big.NewInt(100), big.NewInt(1e18))
	defaultStake     = new(big.Int).Mul(big.NewInt(5), big.NewInt(1e18))
)

// Vec8 converts a capability vector to the fixed int256[8] shape the registry stores.
func Vec8(v capability.Vector) [8]*big.Int {
	on := v.ToOnChain()
	var out [8]*big.Int
	for i := 0; i < 8; i++ {
		out[i] = big.NewInt(on[i])
	}
	return out
}

// BootstrapAgent funds, registers and stakes one agent so it can be routed work.
func (c *Coordinator) BootstrapAgent(spec AgentSpec) (*agent.Agent, error) {
	key, err := agent.DeriveKey(c.Seed, spec.Index)
	if err != nil {
		return nil, err
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)
	cl, err := chain.New(c.Cfg, fmt.Sprintf("%x", crypto.FromECDSA(key)))
	if err != nil {
		return nil, err
	}

	if err := c.fundETH(addr); err != nil {
		return nil, err
	}
	if err := c.fundHive(addr); err != nil {
		return nil, err
	}

	registered, err := cl.IsRegistered(addr)
	if err != nil {
		return nil, err
	}
	if !registered {
		vec := capability.FromSkills(spec.Skills)
		deadline := big.NewInt(time.Now().Add(time.Hour).Unix())
		if _, err := cl.Send("CapabilityRegistry", "registerAgent", addr, Vec8(vec), spec.Price, spec.Endpoint, deadline, []byte{}); err != nil {
			return nil, fmt.Errorf("register agent %d: %w", spec.Index, err)
		}
	}

	if err := c.ensureStake(cl, addr); err != nil {
		return nil, err
	}

	a := &agent.Agent{
		Index:    spec.Index,
		Key:      key,
		Address:  addr,
		Skills:   spec.Skills,
		Price:    spec.Price,
		Endpoint: spec.Endpoint,
		Mode:     spec.Mode,
	}
	c.mu.Lock()
	c.Agents = append(c.Agents, a)
	c.ClientOf[addr.Hex()] = cl
	c.mu.Unlock()
	return a, nil
}

func (c *Coordinator) fundETH(addr common.Address) error {
	bal, err := c.Admin.BalanceWei(addr)
	if err != nil {
		return err
	}
	if bal.Cmp(ethFundPerAgent) >= 0 {
		return nil
	}
	need := new(big.Int).Sub(ethFundPerAgent, bal)
	return c.Admin.TransferEth(addr, need)
}

func (c *Coordinator) fundHive(addr common.Address) error {
	bal, err := c.Admin.HiveBalance(addr)
	if err != nil {
		return err
	}
	if bal.Cmp(hiveFundPerAgent) >= 0 {
		return nil
	}
	need := new(big.Int).Sub(hiveFundPerAgent, bal)
	return c.Admin.TransferHive(addr, need)
}

func (c *Coordinator) ensureStake(cl *chain.Client, addr common.Address) error {
	st, err := cl.StakeOf(addr)
	if err != nil {
		return err
	}
	if st.Cmp(defaultStake) >= 0 {
		return nil
	}
	need := new(big.Int).Sub(defaultStake, st)
	if err := cl.ApproveHive(c.Admin.SettlementAddress(), need); err != nil {
		return err
	}
	return cl.Stake(need)
}

// RefreshViews rebuilds the capability index and routing views from authoritative chain state.
func (c *Coordinator) RefreshViews() error {
	agents, err := c.Admin.AllAgents()
	if err != nil {
		return err
	}
	idx := hnsw.New(8, 32, 32, capability.Dim, 42)
	floor, _ := c.Admin.SettlementMinStake()
	views := map[string]routing.AgentView{}
	for _, a := range agents {
		active, err := c.Admin.IsActive(a)
		if err != nil {
			return err
		}
		if !active {
			continue
		}
		capv, err := c.Admin.CapabilityOf(a)
		if err != nil {
			return err
		}
		vals := make([]int64, 8)
		for i := 0; i < 8; i++ {
			vals[i] = capv[i].Int64()
		}
		vec := capability.FromOnChain(vals)
		price, _ := c.Admin.PriceOf(a)
		score, _ := c.Admin.EffectiveScore(a)
		stake, _ := c.Admin.AvailableStake(a)
		// The contract rejects assignments from agents below the stake floor, so never route to them.
		required := big.NewInt(0)
		if floor != nil {
			required = floor
		}
		if ms, err := c.Admin.MinStakeOf(a); err == nil && ms != nil && ms.Cmp(required) > 0 {
			required = ms
		}
		if stake == nil || stake.Cmp(required) < 0 {
			continue
		}
		ep, _ := c.Admin.EndpointOf(a)
		v := routing.AgentView{
			Address:    a.Hex(),
			Capability: vec,
			Reputation: repToFloat(score),
			Price:      weiToHive(price),
			MaxPrice:   weiToHive(price),
			Load:       0,
			Stake:      weiToHive(stake),
			Endpoint:   ep,
		}
		views[a.Hex()] = v
		idx.Insert(a.Hex(), vec)
	}
	c.mu.Lock()
	c.Index = idx
	c.Views = views
	c.mu.Unlock()
	return nil
}

func repToFloat(x *big.Int) float64 {
	if x == nil {
		return 0
	}
	f := new(big.Float).SetInt(x)
	g := new(big.Float).Quo(f, big.NewFloat(1e18))
	out, _ := g.Float64()
	return out
}

func weiToHive(x *big.Int) float64 {
	if x == nil {
		return 0
	}
	f := new(big.Float).SetInt(x)
	g := new(big.Float).Quo(f, big.NewFloat(1e18))
	out, _ := g.Float64()
	return out
}
