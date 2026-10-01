// Neural Hive - Truffle configuration
// Reads project-root .env so the same files work against Ganache (devnet) or Avalanche C-Chain.
const fs = require("fs");
const path = require("path");
const HDWalletProvider = require("@truffle/hdwallet-provider");

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
const CHAIN_ID = parseInt(process.env.CHAIN_ID || "1337", 10);
const KEYS = [process.env.PRIVATE_KEY_1, process.env.PRIVATE_KEY_2].filter(Boolean);

module.exports = {
  networks: {
    ganache: {
      host: "127.0.0.1",
      port: 8545,
      network_id: "*",
      networkCheckTimeout: 60000,
      timeoutBlocks: 200,
      skipDryRun: true,
      gas: 8000000,
      gasPrice: 2000000000
    },
    avalanche: {
      provider: function () {
        return new HDWalletProvider({ privateKeys: KEYS, providerOrUrl: RPC_URL });
      },
      network_id: "*",
      skipDryRun: true
    }
  },
  compilers: {
    solc: {
      version: "0.8.20",
      settings: {
        optimizer: { enabled: true, runs: 200 },
        evmVersion: "shanghai",
        viaIR: true
      }
    }
  },
  mocha: { timeout: 120000 },
  contracts_directory: "./contracts",
  contracts_build_directory: "./build/contracts",
  migrations_directory: "./migrations",
  test_directory: "./test"
};
