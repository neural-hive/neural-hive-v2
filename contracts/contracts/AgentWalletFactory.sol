// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {AgentWallet} from "./AgentWallet.sol";

/// @title AgentWalletFactory
/// @notice Deploys one AgentWallet smart contract per agent. Each wallet is owned by the agent
///         owner and holds that agent HIVE. The factory records the deployed address per agent
///         id so settlements can route payouts into the agent own wallet.
contract AgentWalletFactory {
    address public immutable hive;

    mapping(bytes32 => address) public walletOf;
    address[] public wallets;

    event WalletCreated(string agentId, address indexed owner, address indexed wallet);

    constructor(address hive_) {
        hive = hive_;
    }

    function idKey(string memory agentId) public pure returns (bytes32) {
        return keccak256(bytes(agentId));
    }

    /// @notice Deploys a wallet for an agent if it has none. Returns the wallet address.
    function createWallet(address owner, string memory agentId) external returns (address) {
        bytes32 k = idKey(agentId);
        require(walletOf[k] == address(0), "wallet already exists for agent");
        AgentWallet w = new AgentWallet(owner, hive, agentId);
        walletOf[k] = address(w);
        wallets.push(address(w));
        emit WalletCreated(agentId, owner, address(w));
        return address(w);
    }

    function walletCount() external view returns (uint256) {
        return wallets.length;
    }
}
