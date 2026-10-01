// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {ECDSA} from "@openzeppelin/contracts/utils/cryptography/ECDSA.sol";

/// @title AttestationLib
/// @notice EIP-712 typed-data digests + signature recovery for Neural Hive agent attestations.
/// @dev This is the trust boundary between off-chain AI agents and the on-chain protocol:
///      an agent response is only accepted if it carries a valid ECDSA signature produced by
///      the signing key that the agent bound to its on-chain identity at registration time.
library AttestationLib {
    using ECDSA for bytes32;

    bytes32 internal constant DOMAIN_TYPEHASH =
        keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)");

    bytes32 internal constant RESPONSE_TYPEHASH = keccak256(
        "ResponseAttestation(uint256 requestId,uint256 stepId,address agent,bytes32 outputHash,int256 outputValue,uint256 nonce,uint256 deadline)"
    );

    bytes32 internal constant REGISTRATION_TYPEHASH = keccak256(
        "AgentRegistration(address operator,address signer,uint256 nonce,uint256 deadline)"
    );

    bytes32 internal constant TRUST_TYPEHASH = keccak256(
        "TrustSignal(address source,address subject,uint256 epoch,uint256 rating)"
    );

    bytes32 internal constant NAME_HASH = keccak256("NeuralHive");
    bytes32 internal constant VERSION_HASH = keccak256("1");

    function domainSeparator(uint256 chainId, address verifyingContract) internal pure returns (bytes32) {
        return keccak256(abi.encode(DOMAIN_TYPEHASH, NAME_HASH, VERSION_HASH, chainId, verifyingContract));
    }

    function responseStructHash(
        uint256 requestId,
        uint256 stepId,
        address agent,
        bytes32 outputHash,
        int256 outputValue,
        uint256 nonce,
        uint256 deadline
    ) internal pure returns (bytes32) {
        return keccak256(abi.encode(RESPONSE_TYPEHASH, requestId, stepId, agent, outputHash, outputValue, nonce, deadline));
    }

    function registrationStructHash(address operator, address signer, uint256 nonce, uint256 deadline)
        internal
        pure
        returns (bytes32)
    {
        return keccak256(abi.encode(REGISTRATION_TYPEHASH, operator, signer, nonce, deadline));
    }

    function trustStructHash(address source, address subject, uint256 epoch, uint256 rating)
        internal
        pure
        returns (bytes32)
    {
        return keccak256(abi.encode(TRUST_TYPEHASH, source, subject, epoch, rating));
    }

    function digest(bytes32 domainSep, bytes32 structHash) internal pure returns (bytes32) {
        return keccak256(abi.encodePacked(hex"1901", domainSep, structHash));
    }

    /// @notice Recover the signer address for a response attestation. Reverts on malformed signatures.
    function recoverResponseSigner(
        bytes32 domainSep,
        uint256 requestId,
        uint256 stepId,
        address agent,
        bytes32 outputHash,
        int256 outputValue,
        uint256 nonce,
        uint256 deadline,
        bytes calldata signature
    ) internal pure returns (address) {
        bytes32 sh = responseStructHash(requestId, stepId, agent, outputHash, outputValue, nonce, deadline);
        return digest(domainSep, sh).recover(signature);
    }

    function recoverRegistrationSigner(
        bytes32 domainSep,
        address operator,
        address signer,
        uint256 nonce,
        uint256 deadline,
        bytes calldata signature
    ) internal pure returns (address) {
        bytes32 sh = registrationStructHash(operator, signer, nonce, deadline);
        return digest(domainSep, sh).recover(signature);
    }
}
