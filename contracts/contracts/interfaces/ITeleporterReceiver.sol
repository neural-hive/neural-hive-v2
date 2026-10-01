// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/// @title ITeleporterReceiver
/// @notice Avalanche ICM (Interchain Messaging / Teleporter) receiver interface.
/// @dev A contract implementing this interface can be called by the TeleporterMessenger when a
///      message arrives from another Avalanche L1. Neural Hive uses it so any L1 can submit a
///      request to the C-Chain contracts and receive a verified answer back, with no bridge.
interface ITeleporterReceiver {
    function receiveTeleporterMessage(
        bytes32 sourceBlockchainID,
        address originSenderAddress,
        bytes calldata message
    ) external;
}
