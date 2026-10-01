// Package config loads Neural Hive runtime configuration from the project .env file and the
// environment, so the same binaries run against Ganache or Avalanche C-Chain unchanged.
//
// Contract addresses can come from two places: the *_ADDRESS variables in .env, or from the
// contracts/deployments.json file written by the Truffle migration. When a local deployment
// file exists it wins, so re-deploying never requires hand-editing .env.
package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved runtime configuration.
type Config struct {
	Root           string
	RPCURL         string
	ChainID        int64
	Network        string
	PrivateKey1    string
	PrivateKey2    string
	Address1       string
	Address2       string
	Contracts      Contracts
	CoordinatorURL string
	RelayerURL     string
	AgentURLs      []string
	ProtocolFeeBps uint64
	EmbeddingDim   int
	Ollama         Ollama
	DeepSeek       DeepSeek
	Hub            Hub
}

// Ollama is the reusable model-backend configuration. Every agent that answers with a language
// model reads it from here, so overriding OLLAMA_BASE_URL and OLLAMA_MODEL repoints all of them
// without a code change.
type Ollama struct {
	// BaseURL is the HTTP endpoint of the Ollama server (OLLAMA_BASE_URL).
	BaseURL string
	// Model is the model tag to run, for example llama3.2:latest (OLLAMA_MODEL).
	Model string
	// AgentURL is the HTTP endpoint of the Neural Hive Ollama agent service (OLLAMA_AGENT_URL).
	AgentURL string
	// Timeout bounds a single generation request (OLLAMA_TIMEOUT_SECONDS).
	Timeout time.Duration
}

// DeepSeek is the reusable DeepSeek model-backend configuration. Every agent that answers
// with DeepSeek reads it from here, so overriding DEEPSEEK_BASE_URL and DEEPSEEK_MODEL
// repoints all of them without a code change. The API key is server-side only.
type DeepSeek struct {
	// APIKey authenticates DeepSeek API calls (DEEPSEEK_API_KEY). Server-side only, never exposed.
	APIKey string
	// BaseURL is the DeepSeek API endpoint (DEEPSEEK_BASE_URL).
	BaseURL string
	// Model is the chat model to run, for example deepseek-chat (DEEPSEEK_MODEL).
	Model string
	// AgentURL is the HTTP endpoint of the Neural Hive DeepSeek agent (DEEPSEEK_AGENT_URL).
	AgentURL string
	// AgentIndex is the deterministic index the DeepSeek agent registers with (DEEPSEEK_AGENT_INDEX).
	AgentIndex int
	// Timeout bounds a single completion request (DEEPSEEK_TIMEOUT_SECONDS).
	Timeout time.Duration
}

// Contracts holds the deployed protocol addresses.
type Contracts struct {
	HiveToken               string
	CapabilityRegistry      string
	ReputationRegistry      string
	StakingSettlement       string
	TaskCoordinator         string
	HiveSnowball            string
	MockTeleporterMessenger string
}

// FindRoot walks upward from the working directory looking for the project root.
func FindRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, "deployments.json")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("project root (containing .env) not found")
}

// LoadEnvFile parses a simple KEY=VALUE file into the process environment without overwriting
// variables that are already set.
func LoadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}

// deploymentFile is the subset of contracts/deployments.json the Go services need.
type deploymentFile struct {
	Network   string            `json:"network"`
	Contracts map[string]string `json:"contracts"`
}

// ApplyDeploymentOverrides reads contracts/deployments.json (written by the Truffle migration)
// and overrides the configured addresses so the Go services always target the most recent local
// deployment. A deployment tagged "test" is ignored, because truffle test deploys to a
// throwaway network whose contracts disappear afterwards.
func ApplyDeploymentOverrides(root string, c *Contracts) {
	path := filepath.Join(root, "contracts", "deployments.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		path = filepath.Join(root, "deployments.json")
		raw, err = os.ReadFile(path)
		if err != nil {
			return
		}
	}
	var d deploymentFile
	if err := json.Unmarshal(raw, &d); err != nil {
		return
	}
	if strings.EqualFold(d.Network, "test") {
		return
	}
	set := func(dst *string, key string) {
		if v := d.Contracts[key]; v != "" {
			*dst = v
		}
	}
	set(&c.HiveToken, "HiveToken")
	set(&c.CapabilityRegistry, "CapabilityRegistry")
	set(&c.ReputationRegistry, "ReputationRegistry")
	set(&c.StakingSettlement, "StakingSettlement")
	set(&c.TaskCoordinator, "TaskCoordinator")
	set(&c.HiveSnowball, "HiveSnowball")
	set(&c.MockTeleporterMessenger, "MockTeleporterMessenger")
}

// deploymentAdmin returns the admin (deployer) address recorded by the Truffle migration. The Hub
// uses it as the default HIVE treasury when neither HIVE_TREASURY_ADDRESS nor ADDRESS_1 is set.
func deploymentAdmin(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "contracts", "deployments.json"))
	if err != nil {
		return ""
	}
	var d struct {
		Admin string `json:"admin"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return ""
	}
	return d.Admin
}

// Load reads configuration from the project .env file (if present) plus the environment.
func Load() (*Config, error) {
	root, err := FindRoot()
	if err != nil {
		return nil, err
	}
	_ = LoadEnvFile(filepath.Join(root, ".env"))

	cfg := &Config{
		Root:           root,
		RPCURL:         get("RPC_URL", "http://127.0.0.1:8545"),
		Network:        get("NETWORK", "ganache"),
		PrivateKey1:    get("PRIVATE_KEY_1", ""),
		PrivateKey2:    get("PRIVATE_KEY_2", ""),
		Address1:       get("ADDRESS_1", ""),
		Address2:       get("ADDRESS_2", ""),
		CoordinatorURL: get("COORDINATOR_API_URL", "http://127.0.0.1:9200"),
		RelayerURL:     get("RELAYER_URL", "http://127.0.0.1:9300"),
		ProtocolFeeBps: uint64(getInt("PROTOCOL_FEE_BPS", 700)),
		EmbeddingDim:   getInt("EMBEDDING_DIM", 8),
		Contracts: Contracts{
			HiveToken:               get("HIVE_TOKEN_ADDRESS", ""),
			CapabilityRegistry:      get("CAPABILITY_REGISTRY_ADDRESS", ""),
			ReputationRegistry:      get("REPUTATION_REGISTRY_ADDRESS", ""),
			StakingSettlement:       get("STAKING_SETTLEMENT_ADDRESS", ""),
			TaskCoordinator:         get("TASK_COORDINATOR_ADDRESS", ""),
			HiveSnowball:            get("HIVE_SNOWBALL_ADDRESS", ""),
			MockTeleporterMessenger: get("MOCK_TELEPORTER_ADDRESS", ""),
		},
		Ollama: Ollama{
			BaseURL:  get("OLLAMA_BASE_URL", "http://127.0.0.1:11434"),
			Model:    get("OLLAMA_MODEL", "llama3.2:latest"),
			AgentURL: get("OLLAMA_AGENT_URL", "http://127.0.0.1:9106"),
			Timeout:  time.Duration(getInt("OLLAMA_TIMEOUT_SECONDS", 180)) * time.Second,
		},
	}

	cfg.DeepSeek = DeepSeek{
		APIKey:     get("DEEPSEEK_API_KEY", ""),
		BaseURL:    get("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		Model:      get("DEEPSEEK_MODEL", "deepseek-chat"),
		AgentURL:   get("DEEPSEEK_AGENT_URL", "http://127.0.0.1:9107"),
		AgentIndex: getInt("DEEPSEEK_AGENT_INDEX", 6),
		Timeout:    time.Duration(getInt("DEEPSEEK_TIMEOUT_SECONDS", 120)) * time.Second,
	}
	ApplyDeploymentOverrides(root, &cfg.Contracts)
	cfg.Hub = loadHub()
	cfg.Hub.TokenAddress = cfg.Contracts.HiveToken
	cfg.Hub.RPCURL = cfg.RPCURL
	cfg.Hub.Network = cfg.Network
	if cfg.Hub.Treasury == "" {
		cfg.Hub.Treasury = cfg.Address1
	}
	if cfg.Hub.Treasury == "" {
		cfg.Hub.Treasury = deploymentAdmin(root)
	}

	cfg.ChainID = int64(getInt("CHAIN_ID", 1337))
	cfg.Hub.ChainID = cfg.ChainID
	for _, key := range []string{"AGENT_1_URL", "AGENT_2_URL", "AGENT_3_URL", "AGENT_4_URL", "AGENT_5_URL", "AGENT_6_URL", "AGENT_7_URL", "AGENT_8_URL"} {
		if v := get(key, ""); v != "" {
			cfg.AgentURLs = append(cfg.AgentURLs, v)
		}
	}
	return cfg, nil
}

func get(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	v := get(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}
