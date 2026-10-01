// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/security/ReentrancyGuard.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {CapabilityRegistry} from "./CapabilityRegistry.sol";
import {StakingSettlement} from "./StakingSettlement.sol";
import {HiveToken} from "./HiveToken.sol";
import {TaskCoordinator} from "./TaskCoordinator.sol";

/// @title HiveSnowball
/// @notice Dispute resolution modelled on Avalanche consensus (Team Rocket, 2018): a small random
///         verifier sample is polled repeatedly; confidence in a preference grows each consecutive
///         round it survives, and the dispute finalises at a confidence threshold. Proposal 4.2.8.
/// @dev A challenger stakes HIVE to contest a finalised step. Verifier agents vote for the output
///      they believe is correct (recomputing the work). If the snowball converges on a preference
///      different from the on-chain consensus, the challenged agent is slashed and the challenger
///      is compensated; otherwise the challenger forfeits the challenge stake to the winner.
contract HiveSnowball is AccessControl, ReentrancyGuard {
    using SafeERC20 for HiveToken;

    bytes32 public constant SNOWBALL_ADMIN_ROLE = keccak256("SNOWBALL_ADMIN_ROLE");

    CapabilityRegistry public immutable registry;
    StakingSettlement public immutable settlement;
    HiveToken public immutable hive;
    TaskCoordinator public coordinator;

    uint256 public beta = 5;
    uint256 public sampleK = 3;
    uint256 public roundLimit = 50;
    uint256 public challengeStake = 1 ether;
    uint256 public slashAmount = 0;

    uint8 internal constant STATUS_OPEN = 0;
    uint8 internal constant STATUS_UPHELD = 1;
    uint8 internal constant STATUS_REJECTED = 2;

    struct Vote {
        address verifier;
        bytes32 preference;
    }

    struct Dispute {
        uint256 stepId;
        address challenger;
        address challengedAgent;
        bytes32 originalPreference;
        bytes32 currentPreference;
        uint256 confidence;
        uint256 round;
        uint8 status;
        bool resolved;
    }

    mapping(uint256 => Dispute) private _dispute;
    mapping(uint256 => Vote[]) private _votes;
    mapping(uint256 => mapping(address => bool)) public hasVoted;
    uint256 public nextDisputeId;

    event DisputeOpened(uint256 indexed disputeId, uint256 indexed stepId, address indexed challenger, address challengedAgent, bytes32 originalPreference);
    event VoteCast(uint256 indexed disputeId, address indexed verifier, bytes32 preference);
    event SnowballRound(uint256 indexed disputeId, uint256 round, uint256 sampleSize, bytes32 preference, uint256 confidence);
    event DisputeResolved(uint256 indexed disputeId, uint8 status, bytes32 finalPreference, uint256 confidence);
    event ParamsUpdated(uint256 beta, uint256 sampleK, uint256 challengeStake);
    event CoordinatorUpdated(address coordinator);

    error UnknownDispute(uint256 disputeId);
    error DisputeClosed(uint256 disputeId);
    error NotRegisteredVerifier(address caller);
    error ChallengerIsWinner(address caller);
    error StepNotFinalized(uint256 stepId, uint8 status);
    error AlreadyVoted(uint256 disputeId, address verifier);
    error ConfidenceNotReached(uint256 confidence, uint256 beta);
    error RoundLimitReached(uint256 round);
    error NoVotes(uint256 disputeId);
    error ZeroAddress();

    constructor(
        address admin,
        CapabilityRegistry capabilityRegistry,
        StakingSettlement stakingSettlement,
        HiveToken hiveToken
    ) {
        if (admin == address(0)) revert ZeroAddress();
        registry = capabilityRegistry;
        settlement = stakingSettlement;
        hive = hiveToken;
        _grantRole(DEFAULT_ADMIN_ROLE, admin);
        _grantRole(SNOWBALL_ADMIN_ROLE, admin);
    }

    function setCoordinator(TaskCoordinator c) external onlyRole(SNOWBALL_ADMIN_ROLE) {
        if (address(c) == address(0)) revert ZeroAddress();
        coordinator = c;
        emit CoordinatorUpdated(address(c));
    }

    function setParams(uint256 beta_, uint256 sampleK_, uint256 roundLimit_, uint256 challengeStake_, uint256 slashAmount_)
        external
        onlyRole(SNOWBALL_ADMIN_ROLE)
    {
        require(beta_ >= 1 && sampleK_ >= 1 && roundLimit_ >= 1, "bad params");
        beta = beta_;
        sampleK = sampleK_;
        roundLimit = roundLimit_;
        challengeStake = challengeStake_;
        slashAmount = slashAmount_;
        emit ParamsUpdated(beta_, sampleK_, challengeStake_);
    }

    /// @notice Contest a finalised step. The challenger must be a registered agent and stakes HIVE.
    function openDispute(uint256 stepId, bytes32 challengerPreference)
        external
        nonReentrant
        returns (uint256 disputeId)
    {
        if (!registry.isRegistered(msg.sender)) revert NotRegisteredVerifier(msg.sender);
        TaskCoordinator.Step memory st = coordinator.stepInfo(stepId);
        if (st.status != 2) revert StepNotFinalized(stepId, st.status);
        if (st.winner == msg.sender) revert ChallengerIsWinner(msg.sender);
        bytes32 consensusHash = st.consensusHash;
        address winner = st.winner;

        if (challengeStake > 0) {
            hive.safeTransferFrom(msg.sender, address(this), challengeStake);
        }

        disputeId = nextDisputeId++;
        Dispute storage d = _dispute[disputeId];
        d.stepId = stepId;
        d.challenger = msg.sender;
        d.challengedAgent = winner;
        d.originalPreference = consensusHash;
        d.currentPreference = consensusHash;
        d.confidence = 0;
        d.round = 0;
        d.status = STATUS_OPEN;

        emit DisputeOpened(disputeId, stepId, msg.sender, winner, consensusHash);
        if (challengerPreference != consensusHash) {
            _castVote(disputeId, msg.sender, challengerPreference);
        }
    }

    /// @notice A verifier records which output it believes is correct.
    function castVote(uint256 disputeId, bytes32 preference) external {
        _castVote(disputeId, msg.sender, preference);
    }

    function _castVote(uint256 disputeId, address verifier, bytes32 preference) internal {
        Dispute storage d = _dispute[disputeId];
        if (d.challenger == address(0)) revert UnknownDispute(disputeId);
        if (d.resolved) revert DisputeClosed(disputeId);
        if (!registry.isRegistered(verifier)) revert NotRegisteredVerifier(verifier);
        require(verifier != d.challengedAgent, "winner cannot vote");
        if (hasVoted[disputeId][verifier]) revert AlreadyVoted(disputeId, verifier);
        hasVoted[disputeId][verifier] = true;
        _votes[disputeId].push(Vote({verifier: verifier, preference: preference}));
        emit VoteCast(disputeId, verifier, preference);
    }

    /// @notice Advance the metastable consensus by one round: poll a fresh random verifier sample.
    function snowballRound(uint256 disputeId) external nonReentrant returns (bytes32 preference) {
        Dispute storage d = _dispute[disputeId];
        if (d.challenger == address(0)) revert UnknownDispute(disputeId);
        if (d.resolved) revert DisputeClosed(disputeId);
        if (d.round >= roundLimit) revert RoundLimitReached(d.round);
        Vote[] storage vs = _votes[disputeId];
        if (vs.length == 0) revert NoVotes(disputeId);

        uint256 k = sampleK;
        if (k > vs.length) k = vs.length;

        bytes32[] memory sample = new bytes32[](k);
        uint256 seed = uint256(keccak256(abi.encodePacked(block.prevrandao, block.timestamp, disputeId, d.round, vs.length)));
        bool[] memory used = new bool[](vs.length);
        for (uint256 i = 0; i < k; i++) {
            uint256 idx = uint256(keccak256(abi.encodePacked(seed, i))) % vs.length;
            while (used[idx]) {
                idx = (idx + 1) % vs.length;
            }
            used[idx] = true;
            sample[i] = vs[idx].preference;
        }

        bytes32 majority = sample[0];
        uint256 bestCount = 0;
        for (uint256 i = 0; i < k; i++) {
            uint256 c = 0;
            for (uint256 j = 0; j < k; j++) {
                if (sample[j] == sample[i]) c++;
            }
            if (c > bestCount) {
                bestCount = c;
                majority = sample[i];
            }
        }

        // Break ties toward the incumbent preference so a tied sample resolves deterministically
        // instead of depending on the random order in which the sample was drawn.
        uint256 incumbentCount = 0;
        for (uint256 j = 0; j < k; j++) {
            if (sample[j] == d.currentPreference) incumbentCount++;
        }
        if (incumbentCount >= bestCount) {
            majority = d.currentPreference;
        }
        d.round += 1;
        if (majority == d.currentPreference) {
            d.confidence += 1;
        } else {
            d.currentPreference = majority;
            d.confidence = 1;
        }
        preference = d.currentPreference;
        emit SnowballRound(disputeId, d.round, k, preference, d.confidence);

        if (d.confidence >= beta) {
            _resolve(disputeId);
        }
    }

    /// @notice Finalise a dispute once confidence has reached beta.
    function resolveDispute(uint256 disputeId) external nonReentrant {
        Dispute storage d = _dispute[disputeId];
        if (d.challenger == address(0)) revert UnknownDispute(disputeId);
        if (d.resolved) revert DisputeClosed(disputeId);
        if (d.confidence < beta) revert ConfidenceNotReached(d.confidence, beta);
        _resolve(disputeId);
    }

    function _resolve(uint256 disputeId) internal {
        Dispute storage d = _dispute[disputeId];
        if (d.resolved) return;
        bytes32 finalPref = d.currentPreference;
        if (finalPref != d.originalPreference) {
            d.status = STATUS_UPHELD;
            d.resolved = true;
            coordinator.applyDisputeOutcome(d.stepId, true);
            uint256 amt = slashAmount == 0 ? type(uint256).max : slashAmount;
            settlement.slash(d.challengedAgent, amt, d.challenger, "hive-snowball:uphold");
            if (challengeStake > 0) {
                hive.safeTransfer(d.challenger, challengeStake);
            }
        } else {
            d.status = STATUS_REJECTED;
            d.resolved = true;
            if (challengeStake > 0 && d.challengedAgent != address(0)) {
                hive.safeTransfer(d.challengedAgent, challengeStake);
            }
        }
        emit DisputeResolved(disputeId, d.status, finalPref, d.confidence);
    }

    // ---------------- views ----------------

    function disputeInfo(uint256 disputeId)
        external
        view
        returns (
            uint256 stepId,
            address challenger,
            address challengedAgent,
            bytes32 originalPreference,
            bytes32 currentPreference,
            uint256 confidence,
            uint256 round,
            uint8 status,
            bool resolved
        )
    {
        Dispute storage d = _dispute[disputeId];
        return (
            d.stepId, d.challenger, d.challengedAgent, d.originalPreference, d.currentPreference,
            d.confidence, d.round, d.status, d.resolved
        );
    }

    function voteCount(uint256 disputeId) external view returns (uint256) {
        return _votes[disputeId].length;
    }

    function voteAt(uint256 disputeId, uint256 i) external view returns (address verifier, bytes32 preference) {
        Vote storage v = _votes[disputeId][i];
        return (v.verifier, v.preference);
    }

    function committeeSize() external view returns (uint256) {
        return registry.agentCount();
    }
}
