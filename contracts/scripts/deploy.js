import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";
import { ethers } from "ethers";

// Emulate __dirname to read environment and artifact paths safely
const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

function getEnvValue(key) {
  const paths = [
    path.join(__dirname, "..", "..", ".env"), // Project Root
    path.join(__dirname, "..", ".env"),       // Contracts folder
    path.join(__dirname, ".env")              // Scripts folder
  ];
  for (const file of paths) {
    if (fs.existsSync(file)) {
      const text = fs.readFileSync(file, "utf8");
      for (const raw of text.split(/\r?\n/)) {
        const line = raw.trim();
        if (!line || line.startsWith("#")) continue;
        const idx = line.indexOf("=");
        if (idx < 0) continue;
        if (line.slice(0, idx).trim() === key) return line.slice(idx + 1).trim();
      }
    }
  }
  return null;
}

// Helper to quickly instantiate an Ethers ContractFactory from local hardhat artifacts
function getFactory(contractName, wallet) {
  const artifactPath = path.join(__dirname, "..", "artifacts", "contracts", `${contractName}.sol`, `${contractName}.json`);
  if (!fs.existsSync(artifactPath)) {
    throw new Error(`Compiled artifact not found for ${contractName} at ${artifactPath}. Run 'npx hardhat compile' first!`);
  }
  const artifact = JSON.parse(fs.readFileSync(artifactPath, "utf8"));
  return new ethers.ContractFactory(artifact.abi, artifact.bytecode, wallet);
}

// Helper to wrap contract bindings for easy function invocation
function getContractInstance(contractName, address, wallet) {
  const artifactPath = path.join(__dirname, "..", "artifacts", "contracts", `${contractName}.sol`, `${contractName}.json`);
  const artifact = JSON.parse(fs.readFileSync(artifactPath, "utf8"));
  return new ethers.Contract(address, artifact.abi, wallet);
}

async function main() {
  // Constants from your Truffle deployment
  const INITIAL_SUPPLY = "100000000000000000000000000"; // 100,000,000 HIVE
  const PROTOCOL_FEE_BPS = 700; // 7%

  // 1. Setup pure environment connections
  const rpcUrl = getEnvValue("RPC_URL") || "http://127.0.0";
  const privateKey = getEnvValue("PRIVATE_KEY_1");

  if (!privateKey) {
    throw new Error("Could not find PRIVATE_KEY_1 in your .env file!");
  }

  const provider = new ethers.JsonRpcProvider(rpcUrl);
  const wallet = new ethers.Wallet(privateKey, provider);
  const admin = wallet.address;

  console.log("Deploying Neural Hive as admin:", admin, "on local Avalanche network...");

  // 2. Fetch the baseline nonce EXACTLY ONCE to prevent provider race conditions
  let currentNonce = await provider.getTransactionCount(admin, "pending");
  console.log(`Starting baseline transaction count tracked at nonce: ${currentNonce}`);

  // 3. Deploy Contracts sequentially while updating our local tracking pointer
  console.log("Deploying HiveToken...");
  const HiveTokenF = getFactory("HiveToken", wallet);
  const hiveToken = await HiveTokenF.deploy(admin, INITIAL_SUPPLY, { nonce: currentNonce++ });
  await hiveToken.waitForDeployment();
  const hiveAddress = await hiveToken.getAddress();

  console.log("Deploying CapabilityRegistry...");
  const CapabilityRegistryF = getFactory("CapabilityRegistry", wallet);
  const capabilityRegistry = await CapabilityRegistryF.deploy(admin, { nonce: currentNonce++ });
  await capabilityRegistry.waitForDeployment();
  const registryAddress = await capabilityRegistry.getAddress();

  console.log("Deploying ReputationRegistry...");
  const ReputationRegistryF = getFactory("ReputationRegistry", wallet);
  const reputationRegistry = await ReputationRegistryF.deploy(admin, { nonce: currentNonce++ });
  await reputationRegistry.waitForDeployment();
  const reputationAddress = await reputationRegistry.getAddress();

  console.log("Deploying StakingSettlement...");
  const StakingSettlementF = getFactory("StakingSettlement", wallet);
  const stakingSettlement = await StakingSettlementF.deploy(admin, hiveAddress, admin, { nonce: currentNonce++ });
  await stakingSettlement.waitForDeployment();
  const settlementAddress = await stakingSettlement.getAddress();

  console.log("Deploying TaskCoordinator...");
  const TaskCoordinatorF = getFactory("TaskCoordinator", wallet);
  const taskCoordinator = await TaskCoordinatorF.deploy(admin, registryAddress, reputationAddress, settlementAddress, PROTOCOL_FEE_BPS, { nonce: currentNonce++ });
  await taskCoordinator.waitForDeployment();
  const coordinatorAddress = await taskCoordinator.getAddress();

  console.log("Deploying HiveSnowball...");
  const HiveSnowballF = getFactory("HiveSnowball", wallet);
  const hiveSnowball = await HiveSnowballF.deploy(admin, registryAddress, settlementAddress, hiveAddress, { nonce: currentNonce++ });
  await hiveSnowball.waitForDeployment();
  const snowballAddress = await hiveSnowball.getAddress();

  console.log("Deploying MockTeleporterMessenger...");
  const MockTeleporterMessengerF = getFactory("MockTeleporterMessenger", wallet);
  const mockTeleporter = await MockTeleporterMessengerF.deploy({ nonce: currentNonce++ });
  await mockTeleporter.waitForDeployment();
  const teleporterAddress = await mockTeleporter.getAddress();

  console.log("Deploying AgentWalletFactory...");
  const AgentWalletFactoryF = getFactory("AgentWalletFactory", wallet);
  const agentWalletFactory = await AgentWalletFactoryF.deploy(hiveAddress, { nonce: currentNonce++ });
  await agentWalletFactory.waitForDeployment();
  const walletFactoryAddress = await agentWalletFactory.getAddress();

  // 4. Instantiate instances to run the Role Wiring transactions
  console.log("Beginning contract role wiring allocations...");
  const settlement = getContractInstance("StakingSettlement", settlementAddress, wallet);
  const registry = getContractInstance("CapabilityRegistry", registryAddress, wallet);
  const reputation = getContractInstance("ReputationRegistry", reputationAddress, wallet);
  const coordinator = getContractInstance("TaskCoordinator", coordinatorAddress, wallet);
  const snowball = getContractInstance("HiveSnowball", snowballAddress, wallet);

  // Execute Role Wirings sequentially with exact custom tracking nonces
  let tx;
  
  tx = await settlement.grantRole(await settlement.SETTLEMENT_ROLE(), coordinatorAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  tx = await settlement.grantRole(await settlement.SLASHER_ROLE(), snowballAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  tx = await registry.grantRole(await registry.OUTCOME_ROLE(), coordinatorAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  tx = await reputation.grantRole(await reputation.ORACLE_ROLE(), coordinatorAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  tx = await reputation.grantRole(await reputation.SIGNAL_ROLE(), coordinatorAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  tx = await reputation.grantRole(await reputation.ARBITER_ROLE(), snowballAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  tx = await coordinator.grantRole(await coordinator.SNOWBALL_ROLE(), snowballAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  // Execute state adjustments with custom increments
  tx = await coordinator.setSnowball(snowballAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  tx = await coordinator.setTeleporterMessenger(teleporterAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  tx = await snowball.setCoordinator(coordinatorAddress, { nonce: currentNonce++ }); 
  await tx.wait();

  console.log("Role wiring completed successfully.");

  // 5. Output configuration parameters matching your exact JSON structure
  const deployment = {
    network: "avalanche_local",
    admin: admin,
    deployedAt: new Date().toISOString(),
    protocolFeeBps: PROTOCOL_FEE_BPS,
    contracts: {
      HiveToken: hiveAddress,
      CapabilityRegistry: registryAddress,
      ReputationRegistry: reputationAddress,
      StakingSettlement: settlementAddress,
      TaskCoordinator: coordinatorAddress,
      HiveSnowball: snowballAddress,
      MockTeleporterMessenger: teleporterAddress,
      AgentWalletFactory: walletFactoryAddress
    }
  };

  const outPath = path.join(__dirname, "..", "deployments.json");
  fs.writeFileSync(outPath, JSON.stringify(deployment, null, 2));
  console.log("Deployment details written to", outPath);
  console.log(JSON.stringify(deployment.contracts, null, 2));
}

main()
  .then(() => process.exit(0))
  .catch((error) => {
    console.error("Deployment failed:", error);
    process.exit(1);
  });
