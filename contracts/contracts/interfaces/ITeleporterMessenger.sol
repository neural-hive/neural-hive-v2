// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/// @title ITeleporterMessenger
/// @notice Minimal outbound side of the ICM/Teleporter interface used by Neural Hive.
interface ITeleporterMessenger {
    function sendCrossChainAnswer(
        bytes32 destinationBlockchainID,
        address destination,
        uint256 requestId,
        bytes32 answerHash
    ) external;
}
