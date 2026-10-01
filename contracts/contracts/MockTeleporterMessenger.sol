// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {ITeleporterReceiver} from "./interfaces/ITeleporterReceiver.sol";

/// @title MockTeleporterMessenger
/// @notice Local simulation of Avalanche Interchain Messaging (Teleporter) on the Ganache devnet.
/// @dev On a real network the TeleporterMessenger is a pre-deployed Avalanche contract and messages
///      are validated by Warp signatures from the source L1 validators. Here a test relayer plays
///      the role of the Warp validator set: `deliver` pushes an inbound message into a receiver,
///      and outbound answers are recorded so the requester L1 can be observed picking them up.
contract MockTeleporterMessenger {
    struct Outbound {
        bytes32 destinationBlockchainID;
        address destination;
        uint256 requestId;
        bytes32 answerHash;
    }

    Outbound[] private _outbound;

    event CrossChainMessageReceived(bytes32 indexed sourceBlockchainID, address indexed originSender, address indexed receiver);
    event CrossChainAnswerSent(bytes32 indexed destinationBlockchainID, address indexed destination, uint256 indexed requestId, bytes32 answerHash);

    error NoReceiver(address receiver);

    /// @notice Simulate Warp-verified delivery of an inbound ICM message into a receiver contract.
    function deliver(
        bytes32 sourceBlockchainID,
        address originSenderAddress,
        address receiver,
        bytes calldata message
    ) external {
        if (receiver.code.length == 0) revert NoReceiver(receiver);
        emit CrossChainMessageReceived(sourceBlockchainID, originSenderAddress, receiver);
        ITeleporterReceiver(receiver).receiveTeleporterMessage(sourceBlockchainID, originSenderAddress, message);
    }

    /// @notice Outbound side of ICM: Neural Hive sends a verified answer back to the source L1.
    function sendCrossChainAnswer(
        bytes32 destinationBlockchainID,
        address destination,
        uint256 requestId,
        bytes32 answerHash
    ) external {
        _outbound.push(
            Outbound({
                destinationBlockchainID: destinationBlockchainID,
                destination: destination,
                requestId: requestId,
                answerHash: answerHash
            })
        );
        emit CrossChainAnswerSent(destinationBlockchainID, destination, requestId, answerHash);
    }

    function outboundCount() external view returns (uint256) {
        return _outbound.length;
    }

    function outboundAt(uint256 i)
        external
        view
        returns (bytes32 destinationBlockchainID, address destination, uint256 requestId, bytes32 answerHash)
    {
        Outbound storage o = _outbound[i];
        return (o.destinationBlockchainID, o.destination, o.requestId, o.answerHash);
    }
}
