// Neural Hive deployment + role wiring.
// Deploys the full protocol: HIVE token, capability registry, reputation registry,
// staking/settlement, task coordinator, Hive Snowball disputes, and the local ICM messenger.
const fs = require("fs");
const path = require("path");

const HiveToken = artifacts.require("HiveToken");
const CapabilityRegistry = artifacts.require("CapabilityRegistry");
const ReputationRegistry = artifacts.require("ReputationRegistry");
const StakingSettlement = artifacts.require("StakingSettlement");
const TaskCoordinator = artifacts.require("TaskCoordinator");
const HiveSnowball = artifacts.require("HiveSnowball");
const MockTeleporterMessenger = artifacts.require("MockTeleporterMessenger");

const INITIAL_SUPPLY = "100000000000000000000000000"; // 100,000,000 HIVE (illustrative)
const PROTOCOL_FEE_BPS = 700; // 7%

module.exports = async function (deployer, network, accounts) {
  const admin = accounts[0];
  console.log("Deploying Neural Hive as admin", admin, "on", network);

  await deployer.deploy(HiveToken, admin, INITIAL_SUPPLY);
  const hive = await HiveToken.deployed();

  await deployer.deploy(CapabilityRegistry, admin);
  const registry = await CapabilityRegistry.deployed();

  await deployer.deploy(ReputationRegistry, admin);
  const reputation = await ReputationRegistry.deployed();

  await deployer.deploy(StakingSettlement, admin, hive.address, admin);
  const settlement = await StakingSettlement.deployed();

  await deployer.deploy(TaskCoordinator, admin, registry.address, reputation.address, settlement.address, PROTOCOL_FEE_BPS);
  const coordinator = await TaskCoordinator.deployed();

  await deployer.deploy(HiveSnowball, admin, registry.address, settlement.address, hive.address);
  const snowball = await HiveSnowball.deployed();

  await deployer.deploy(MockTeleporterMessenger);
  const teleporter = await MockTeleporterMessenger.deployed();

  // ---- role wiring ----
  await settlement.grantRole(await settlement.SETTLEMENT_ROLE(), coordinator.address);
  await settlement.grantRole(await settlement.SLASHER_ROLE(), snowball.address);
  await registry.grantRole(await registry.OUTCOME_ROLE(), coordinator.address);
  await reputation.grantRole(await reputation.ORACLE_ROLE(), coordinator.address);
  await reputation.grantRole(await reputation.SIGNAL_ROLE(), coordinator.address);
  await reputation.grantRole(await reputation.ARBITER_ROLE(), snowball.address);
  await coordinator.grantRole(await coordinator.SNOWBALL_ROLE(), snowball.address);

  await coordinator.setSnowball(snowball.address);
  await coordinator.setTeleporterMessenger(teleporter.address);
  await snowball.setCoordinator(coordinator.address);

  const deployment = {
    network: network,
    admin: admin,
    deployedAt: new Date().toISOString(),
    protocolFeeBps: PROTOCOL_FEE_BPS,
    contracts: {
      HiveToken: hive.address,
      CapabilityRegistry: registry.address,
      ReputationRegistry: reputation.address,
      StakingSettlement: settlement.address,
      TaskCoordinator: coordinator.address,
      HiveSnowball: snowball.address,
      MockTeleporterMessenger: teleporter.address
    }
  };

  const outPath = path.join(__dirname, "..", "deployments.json");
  fs.writeFileSync(outPath, JSON.stringify(deployment, null, 2));
  console.log("Deployment written to", outPath);
  console.log(JSON.stringify(deployment.contracts, null, 2));
};
