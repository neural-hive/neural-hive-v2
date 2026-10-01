// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {EnumerableSet} from "@openzeppelin/contracts/utils/structs/EnumerableSet.sol";
import {AttestationLib} from "./libraries/AttestationLib.sol";

/// @title CapabilityRegistry
/// @notice On-chain source of truth for every registered Neural Hive agent: its declared
///         capability vector, advertised price, endpoint, and the signing key it uses to
///         authenticate task responses (see AttestationLib).
/// @dev The off-chain HNSW capability index (proposal 4.2.1) is (re)built from this registry and
///      reconciled against it periodically, so routing always reflects the latest on-chain state.
contract CapabilityRegistry is AccessControl {
    using EnumerableSet for EnumerableSet.AddressSet;

    uint256 public constant EMBEDDING_DIM = 8;

    bytes32 public constant REGISTRY_ADMIN_ROLE = keccak256("REGISTRY_ADMIN_ROLE");
    bytes32 public constant OUTCOME_ROLE = keccak256("OUTCOME_ROLE");

    struct Agent {
        address operator;
        address signer;
        int256[EMBEDDING_DIM] capability;
        string endpoint;
        uint256 price;
        uint256 minStake;
        bool active;
        uint64 registeredAt;
        uint256 tasksAssigned;
        uint256 tasksSucceeded;
    }

    EnumerableSet.AddressSet private _agents;
    mapping(address => Agent) private _agent;
    mapping(address => uint256) public registrationNonce;
    mapping(address => bool) private _signerTaken;

    event AgentRegistered(address indexed agent, address indexed signer, uint256 price, string endpoint);
    event CapabilityUpdated(address indexed agent, int256[EMBEDDING_DIM] capability);
    event PriceUpdated(address indexed agent, uint256 price);
    event EndpointUpdated(address indexed agent, string endpoint);
    event SignerRotated(address indexed agent, address indexed newSigner);
    event ActiveFlagSet(address indexed agent, bool active);
    event OutcomeRecorded(address indexed agent, bool success);

    error AlreadyRegistered(address agent);
    error NotRegistered(address agent);
    error BadSignature(address recovered, address expected);
    error SignerInUse(address signer);
    error ZeroAddress();
    error DeadlineExpired(uint256 deadline);
    error PriceTooHigh(uint256 price, uint256 max);

    uint256 public constant MAX_PRICE = 1 ether;

    constructor(address admin) {
        require(admin != address(0), "admin=0");
        _grantRole(DEFAULT_ADMIN_ROLE, admin);
        _grantRole(REGISTRY_ADMIN_ROLE, admin);
        _grantRole(OUTCOME_ROLE, admin);
    }

    function _domainSeparator() internal view returns (bytes32) {
        return AttestationLib.domainSeparator(block.chainid, address(this));
    }

    /// @notice Register a new agent. Anyone may register; trust is bootstrapped by staking + reputation.
    /// @param signer Key that signs task responses. May equal msg.sender.
    /// @dev If signer != msg.sender the call must carry an EIP-712 AgentRegistration signature from signer.
    function registerAgent(
        address signer,
        int256[EMBEDDING_DIM] calldata capability,
        uint256 price,
        string calldata endpoint,
        uint256 deadline,
        bytes calldata signature
    ) external {
        address operator = msg.sender;
        if (_agent[operator].operator != address(0)) revert AlreadyRegistered(operator);
        if (signer == address(0)) revert ZeroAddress();
        if (price > MAX_PRICE) revert PriceTooHigh(price, MAX_PRICE);
        if (block.timestamp > deadline) revert DeadlineExpired(deadline);
        if (_signerTaken[signer]) revert SignerInUse(signer);

        if (signer != operator) {
            uint256 nonce = registrationNonce[operator]++;
            address recovered = AttestationLib.recoverRegistrationSigner(
                _domainSeparator(), operator, signer, nonce, deadline, signature
            );
            if (recovered != signer) revert BadSignature(recovered, signer);
        }

        Agent storage a = _agent[operator];
        a.operator = operator;
        a.signer = signer;
        a.capability = capability;
        a.price = price;
        a.endpoint = endpoint;
        a.active = true;
        a.registeredAt = uint64(block.timestamp);
        _signerTaken[signer] = true;
        _agents.add(operator);

        emit AgentRegistered(operator, signer, price, endpoint);
        emit CapabilityUpdated(operator, capability);
    }

    function updateCapability(int256[EMBEDDING_DIM] calldata capability) external {
        _requireOperator(msg.sender);
        _agent[msg.sender].capability = capability;
        emit CapabilityUpdated(msg.sender, capability);
    }

    function updatePrice(uint256 price) external {
        _requireOperator(msg.sender);
        if (price > MAX_PRICE) revert PriceTooHigh(price, MAX_PRICE);
        _agent[msg.sender].price = price;
        emit PriceUpdated(msg.sender, price);
    }

    function updateEndpoint(string calldata endpoint) external {
        _requireOperator(msg.sender);
        _agent[msg.sender].endpoint = endpoint;
        emit EndpointUpdated(msg.sender, endpoint);
    }

    function setMinStake(uint256 minStake) external {
        _requireOperator(msg.sender);
        _agent[msg.sender].minStake = minStake;
    }

    function setActive(bool active) external {
        _requireOperator(msg.sender);
        _agent[msg.sender].active = active;
        emit ActiveFlagSet(msg.sender, active);
    }

    /// @notice Rotate the response signing key, authorised by the operator with a fresh registration signature.
    function rotateSigner(address newSigner, uint256 deadline, bytes calldata signature) external {
        address operator = msg.sender;
        _requireOperator(operator);
        if (newSigner == address(0)) revert ZeroAddress();
        if (block.timestamp > deadline) revert DeadlineExpired(deadline);
        if (_signerTaken[newSigner]) revert SignerInUse(newSigner);
        if (newSigner != operator) {
            uint256 nonce = registrationNonce[operator]++;
            address recovered = AttestationLib.recoverRegistrationSigner(
                _domainSeparator(), operator, newSigner, nonce, deadline, signature
            );
            if (recovered != newSigner) revert BadSignature(recovered, newSigner);
        }
        address old = _agent[operator].signer;
        _signerTaken[old] = false;
        _signerTaken[newSigner] = true;
        _agent[operator].signer = newSigner;
        emit SignerRotated(operator, newSigner);
    }

    /// @notice Coordinator/settlement hook to keep public counters in sync.
    function recordOutcome(address agent, bool success) external onlyRole(OUTCOME_ROLE) {
        Agent storage a = _agent[agent];
        if (a.operator == address(0)) revert NotRegistered(agent);
        a.tasksAssigned += 1;
        if (success) a.tasksSucceeded += 1;
        emit OutcomeRecorded(agent, success);
    }

    function _requireOperator(address caller) internal view {
        if (_agent[caller].operator == address(0)) revert NotRegistered(caller);
    }

    function isRegistered(address agent) external view returns (bool) {
        return _agent[agent].operator != address(0);
    }

    function isActive(address agent) external view returns (bool) {
        return _agent[agent].active;
    }

    function signerOf(address agent) external view returns (address) {
        return _agent[agent].signer;
    }

    function priceOf(address agent) external view returns (uint256) {
        return _agent[agent].price;
    }

    function minStakeOf(address agent) external view returns (uint256) {
        return _agent[agent].minStake;
    }

    function endpointOf(address agent) external view returns (string memory) {
        return _agent[agent].endpoint;
    }

    function capabilityOf(address agent) external view returns (int256[EMBEDDING_DIM] memory) {
        return _agent[agent].capability;
    }

    function statsOf(address agent) external view returns (uint256 assigned, uint256 succeeded) {
        Agent storage a = _agent[agent];
        return (a.tasksAssigned, a.tasksSucceeded);
    }

    function agentCount() external view returns (uint256) {
        return _agents.length();
    }

    function agentAt(uint256 i) external view returns (address) {
        return _agents.at(i);
    }

    function allAgents() external view returns (address[] memory) {
        return _agents.values();
    }
}
