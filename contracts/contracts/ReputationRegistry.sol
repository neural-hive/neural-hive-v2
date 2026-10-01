// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";

/// @title ReputationRegistry
/// @notice Stores the EigenTrust score (proposal 4.2.6) of every agent, plus the raw trust signals
///         that the off-chain EigenTrust power-iteration is computed from.
/// @dev The EigenTrust computation is O(n^2) and is performed off-chain (Go). Its result is
///      submitted by a REPUTATION_ORACLE as a batch and only becomes canonical after an
///      optimistic CHALLENGE_WINDOW elapses with no successful challenge. A challenge is resolved
///      by the Hive Snowball dispute contract, which can veto the batch.
contract ReputationRegistry is AccessControl {
    bytes32 public constant REPUTATION_ADMIN_ROLE = keccak256("REPUTATION_ADMIN_ROLE");
    bytes32 public constant ORACLE_ROLE = keccak256("ORACLE_ROLE");
    bytes32 public constant SIGNAL_ROLE = keccak256("SIGNAL_ROLE");
    bytes32 public constant ARBITER_ROLE = keccak256("ARBITER_ROLE");

    uint256 public constant SCALE = 1e18;
    uint256 public constant MAX_RATING = 1e18;

    struct Batch {
        address submitter;
        uint256 epoch;
        uint256 proposedAt;
        uint256 challengeDeadline;
        bool challenged;
        bool finalized;
        bool rejected;
        uint256 agentCount;
    }

    mapping(address => uint256) private _score;
    mapping(address => uint256) public lastScoreUpdateEpoch;
    mapping(address => bool) public knownAgent;
    /// @dev True once an agent has been included in a finalised EigenTrust batch.
    mapping(address => bool) public scored;

    uint256 public epoch;
    uint256 public nextBatchId;
    uint256 public challengeWindow = 60; // seconds (testnet default; governance-tunable)

    mapping(uint256 => Batch) public batches;
    mapping(uint256 => address[]) private _batchAgents;
    mapping(uint256 => mapping(address => uint256)) private _batchScores;

    /// @dev (source, subject, epoch) -> already signalled flag, prevents double signals per epoch.
    mapping(address => mapping(address => uint256)) public lastSignalEpoch;

    event TrustSignal(address indexed source, address indexed subject, uint256 indexed epoch, uint256 rating);
    event ScoresProposed(uint256 indexed batchId, address indexed submitter, uint256 epoch, uint256 agentCount);
    event ScoresFinalized(uint256 indexed batchId, uint256 epoch);
    event ScoresChallenged(uint256 indexed batchId, address indexed challenger);
    event ChallengeResolved(uint256 indexed batchId, bool upheld);
    event ChallengeWindowUpdated(uint256 window);
    event EpochAdvanced(uint256 epoch);

    error UnknownBatch(uint256 batchId);
    error BatchAlreadyFinalized(uint256 batchId);
    error ChallengeWindowOpen(uint256 deadline);
    error ChallengeWindowClosed(uint256 deadline);
    error LengthMismatch();
    error RatingOutOfRange(uint256 rating);
    error ZeroAddress();
    error UnknownAgent(address agent);

    constructor(address admin) {
        require(admin != address(0), "admin=0");
        _grantRole(DEFAULT_ADMIN_ROLE, admin);
        _grantRole(REPUTATION_ADMIN_ROLE, admin);
        _grantRole(ORACLE_ROLE, admin);
        _grantRole(SIGNAL_ROLE, admin);
        _grantRole(ARBITER_ROLE, admin);
    }

    // ---------------- trust signals ----------------

    /// @notice Record a local trust observation. Anyone registered may signal; signals are
    ///         emitted so the off-chain EigenTrust builder can reconstruct the trust matrix C.
    function submitTrustSignal(address subject, uint256 rating) external {
        if (subject == address(0)) revert ZeroAddress();
        if (rating > MAX_RATING) revert RatingOutOfRange(rating);
        if (!knownAgent[msg.sender]) revert UnknownAgent(msg.sender);
        if (!knownAgent[subject]) revert UnknownAgent(subject);
        require(lastSignalEpoch[msg.sender][subject] != epoch + 1, "already signalled this epoch");
        lastSignalEpoch[msg.sender][subject] = epoch + 1;
        emit TrustSignal(msg.sender, subject, epoch, rating);
    }

    /// @notice Mark an address as a participant whose reputation is tracked.
    function registerAgent(address agent) external onlyRole(SIGNAL_ROLE) {
        if (agent == address(0)) revert ZeroAddress();
        knownAgent[agent] = true;
    }

    function setEpoch(uint256 newEpoch) external onlyRole(REPUTATION_ADMIN_ROLE) {
        require(newEpoch > epoch, "epoch must advance");
        epoch = newEpoch;
        emit EpochAdvanced(newEpoch);
    }

    function setChallengeWindow(uint256 window) external onlyRole(REPUTATION_ADMIN_ROLE) {
        challengeWindow = window;
        emit ChallengeWindowUpdated(window);
    }

    // ---------------- optimistic EigenTrust batches ----------------

    /// @notice Propose a full EigenTrust score vector for the current epoch.
    function proposeScores(address[] calldata agents, uint256[] calldata scores)
        external
        onlyRole(ORACLE_ROLE)
        returns (uint256 batchId)
    {
        if (agents.length != scores.length) revert LengthMismatch();
        batchId = nextBatchId++;
        Batch storage b = batches[batchId];
        b.submitter = msg.sender;
        b.epoch = epoch;
        b.proposedAt = block.timestamp;
        b.challengeDeadline = block.timestamp + challengeWindow;
        b.agentCount = agents.length;
        for (uint256 i = 0; i < agents.length; i++) {
            _batchAgents[batchId].push(agents[i]);
            _batchScores[batchId][agents[i]] = scores[i];
            knownAgent[agents[i]] = true;
        }
        emit ScoresProposed(batchId, msg.sender, epoch, agents.length);
    }

    /// @notice Apply a proposed batch once its challenge window has elapsed.
    function finalizeScores(uint256 batchId) external {
        Batch storage b = batches[batchId];
        if (b.submitter == address(0)) revert UnknownBatch(batchId);
        if (b.finalized) revert BatchAlreadyFinalized(batchId);
        if (b.challenged) revert BatchAlreadyFinalized(batchId);
        if (block.timestamp <= b.challengeDeadline) revert ChallengeWindowOpen(b.challengeDeadline);
        _apply(batchId);
    }

    /// @notice Challenge a proposed batch inside its window. Blocks finalization until resolved.
    function challengeScores(uint256 batchId) external {
        Batch storage b = batches[batchId];
        if (b.submitter == address(0)) revert UnknownBatch(batchId);
        if (b.finalized) revert BatchAlreadyFinalized(batchId);
        if (block.timestamp > b.challengeDeadline) revert ChallengeWindowClosed(b.challengeDeadline);
        b.challenged = true;
        emit ScoresChallenged(batchId, msg.sender);
    }

    /// @notice Arbiter (Hive Snowball) verdict on a challenged batch.
    function resolveChallenge(uint256 batchId, bool uphold) external onlyRole(ARBITER_ROLE) {
        Batch storage b = batches[batchId];
        if (b.submitter == address(0)) revert UnknownBatch(batchId);
        if (b.finalized) revert BatchAlreadyFinalized(batchId);
        if (uphold) {
            _apply(batchId);
        } else {
            b.rejected = true;
            b.finalized = true;
        }
        emit ChallengeResolved(batchId, uphold);
    }

    function _apply(uint256 batchId) internal {
        Batch storage b = batches[batchId];
        b.finalized = true;
        address[] storage agents = _batchAgents[batchId];
        for (uint256 i = 0; i < agents.length; i++) {
            address a = agents[i];
            _score[a] = _batchScores[batchId][a];
            scored[a] = true;
            lastScoreUpdateEpoch[a] = b.epoch;
        }
        emit ScoresFinalized(batchId, b.epoch);
    }

    // ---------------- views ----------------

    /// @dev Neutral prior for agents that have not yet been scored by an EigenTrust round.
    uint256 public constant PRIOR_SCORE = 1e17; // 0.1 * SCALE

    function scoreOf(address agent) external view returns (uint256) {
        return _score[agent];
    }

    /// @notice Score used by routing, with a neutral prior for unscored agents.
    function effectiveScore(address agent) external view returns (uint256) {
        if (!scored[agent]) return PRIOR_SCORE;
        return _score[agent];
    }

    function batchAgentCount(uint256 batchId) external view returns (uint256) {
        return _batchAgents[batchId].length;
    }

    function batchAgentAt(uint256 batchId, uint256 i) external view returns (address) {
        return _batchAgents[batchId][i];
    }

    function batchScore(uint256 batchId, address agent) external view returns (uint256) {
        return _batchScores[batchId][agent];
    }
}
