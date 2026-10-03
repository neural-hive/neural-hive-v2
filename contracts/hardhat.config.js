import hardhatEthers from "@nomicfoundation/hardhat-ethers";
import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

function loadEnv(file) {
  if (!fs.existsSync(file)) return;
  const text = fs.readFileSync(file, "utf8");
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const idx = line.indexOf("=");
    if (idx < 0) continue;
    const key = line.slice(0, idx).trim();
    const value = line.slice(idx + 1).trim();
    if (process.env[key] === undefined) process.env[key] = value;
  }
}

loadEnv(path.join(__dirname, "..", ".env"));
loadEnv(path.join(__dirname, ".env"));

const RPC_URL = process.env.RPC_URL || "http://127.0.0.1:8545";
const CHAIN_ID = Number(process.env.CHAIN_ID || 31337);

// Anvil's default account #0 (public dev key, only valid on local chains).
const ANVIL_DEFAULT_KEY =
  "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80";

const KEYS = [process.env.PRIVATE_KEY_1, process.env.PRIVATE_KEY_2].filter(Boolean);
const ACCOUNTS = KEYS.length > 0 ? KEYS : [ANVIL_DEFAULT_KEY];

const localNetwork = {
  type: "http",
  url: RPC_URL,
  chainId: CHAIN_ID,
  accounts: ACCOUNTS
};

export default {
  solidity: {
    version: "0.8.20",
    settings: {
      optimizer: { enabled: true, runs: 200 },
      evmVersion: "shanghai",
      viaIR: true
    }
  },
  networks: {
    // Kept under the old name so `--network avalanche` in start.ps1 still works.
    avalanche: localNetwork,
    // Same network under a clearer name.
    anvil: localNetwork
  },
  paths: {
    sources: "./contracts",
    tests: "./test",
    cache: "./cache",
    artifacts: "./artifacts"
  },
  plugins: [hardhatEthers]
};