// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/security/ReentrancyGuard.sol";
import {CapabilityRegistry} from "./CapabilityRegistry.sol";
import {ReputationRegistry} from "./ReputationRegistry.sol";
import {StakingSettlement} from "./StakingSettlement.sol";
import {AttestationLib} from "./libraries/AttestationLib.sol";
import {Krum} from "./libraries/Krum.sol";
import {ITeleporterReceiver} from "./interfaces/ITeleporterReceiver.sol";
import {ITeleporterMessenger} from "./interfaces/ITeleporterMessenger.sol";

/// @title TaskCoordinator
/// @notice On-chain lifecycle for Neural Hive requests: request escrow, step graph registration,
///         agent assignment, signed response collection, Krum aggregation, VCG settlement, and
///         dispute hooks. Implements the on-chain half of proposal section 4.2.
/// @dev Trust model: the off-chain coordinator (router/relayer) may choose assignments and may
///      relay responses, but it CANNOT forge a response: every accepted response must carry a
///      valid EIP-712 signature from the agent signing key bound in CapabilityRegistry. The
///      aggregation rule (Krum) and the payment rule (VCG) are recomputed on-chain, so a
///      malicious relayer cannot substitute a different consensus or a different payout.
contract TaskCoordinator is AccessControl, ReentrancyGuard, ITeleporterReceiver {
    uint256 public constant EMBEDDING_DIM = 8;
    uint256 public constant MAX_COMMITTEE = 64;
    uint256 public constant BPS_DENOMINATOR = 10_000;

    bytes32 public constant COORDINATOR_ROLE = keccak256("COORDINATOR_ROLE");
    bytes32 public constant COORDINATOR_ADMIN_ROLE = keccak256("COORDINATOR_ADMIN_ROLE");
    bytes32 public constant SNOWBALL_ROLE = keccak256("SNOWBALL_ROLE");
    bytes32 public constant PARAMS_ADMIN_ROLE = keccak256("PARAMS_ADMIN_ROLE");

    CapabilityRegistry public immutable registry;
    ReputationRegistry public immutable reputation;
    StakingSettlement public immutable settlement;

    address public snowball;
    address public teleporterMessenger;
    uint256 public protocolFeeBps;

    uint8 internal constant STATUS_OPEN = 0;
    uint8 internal constant STATUS_SETTLED = 1;
    uint8 internal constant STATUS_CANCELLED = 2;

    uint8 internal constant STEP_PENDING = 0;
    uint8 internal constant STEP_COLLECTING = 1;
    uint8 internal constant STEP_FINALIZED = 2;
    uint8 internal constant STEP_FAILED = 3;

    uint8 public constant TIER_OPTIMISTIC = 0;
    uint8 public constant TIER_REPLICATED_KRUM = 1;
    uint8 public constant TIER_ZKML_PROOF = 2;

    struct Request {
        address requester;
        bytes32 metaHash;
        uint256 budget;
        uint256 spent;
        uint64 createdAt;
        uint8 status;
        uint8 tier;
        uint256 stepCount;
        uint256 finalizedCount;
        bytes32 sourceBlockchainID;
        address originSender;
    }

    struct Step {
        uint256 requestId;
        int256[EMBEDDING_DIM] taskVec;
        uint32 replication;
        uint32 f;
        uint32 assigned;
        uint32 responded;
        uint256 maxPrice;
        uint64 deadline;
        uint8 status;
        address winner;
        address runnerUp;
        int256 consensusValue;
        bytes32 consensusHash;
        uint256 payout;
        bool numeric;
        bool settled;
    }

    struct ResponseView {
        address agent;
        bytes32 outputHash;
        int256 value;
        uint256 bid;
        uint64 at;
        bool matchesConsensus;
        bool paid;
    }

    struct ResponseInternal {
        address agent;
        bytes32 outputHash;
        int256 value;
        uint256 bid;
        uint64 at;
    }

    uint256 public nextRequestId;
    uint256 public nextStepId;

    mapping(uint256 => Request) private _request;
    mapping(uint256 => Step) private _step;
    mapping(uint256 => address[]) private _stepAgents;
    mapping(uint256 => mapping(address => uint256)) private _stepBid;
    mapping(uint256 => mapping(address => bool)) private _stepAssigned;
    mapping(uint256 => mapping(address => bool)) private _stepRespondedFlag;
    mapping(uint256 => ResponseInternal[]) private _stepResponses;
    mapping(uint256 => mapping(address => bool)) private _stepPaid;
    mapping(uint256 => mapping(address => uint256)) private _stepConsensusScore;
    mapping(uint256 => uint256[]) private _stepScores;
    mapping(address => mapping(uint256 => bool)) public nonceUsed;

    event RequestCreated(uint256 indexed requestId, address indexed requester, bytes32 metaHash, uint256 budget, uint8 tier);
    event RequestFunded(uint256 indexed requestId, address indexed payer, uint256 amount);
    event StepAdded(uint256 indexed stepId, uint256 indexed requestId, uint32 replication, uint32 f, uint256 maxPrice, uint64 deadline, bool numeric);
    event AgentAssigned(uint256 indexed stepId, address indexed agent, uint256 bid);
    event ResponseAccepted(uint256 indexed stepId, address indexed agent, bytes32 outputHash, int256 value, uint256 nonce);
    event StepFinalized(uint256 indexed stepId, address indexed winner, address runnerUp, int256 consensusValue, bytes32 consensusHash, uint256 payout);
    event StepFailed(uint256 indexed stepId, uint32 responded, uint32 required);
    event RequestSettled(uint256 indexed requestId, uint256 spent, uint256 refunded);
    event PayoutApplied(uint256 indexed requestId, uint256 indexed stepId, address indexed agent, uint256 payment, uint256 protocolFee);
    event AgentOutcomeRecorded(address indexed agent, bool success);
    event DisputeOutcomeApplied(uint256 indexed stepId, address indexed wrongAgent, bool winnerWrong);
    event InterchainRequestReceived(bytes32 indexed sourceBlockchainID, address indexed originSender, uint256 indexed requestId, uint256 budget);
    event InterchainAnswerSent(bytes32 indexed destinationBlockchainID, address indexed destination, uint256 indexed requestId, bytes32 answerHash);
    event ParamsUpdated(uint256 protocolFeeBps);
    event SnowballSet(address snowball);
    event TrustedTeleporterSet(address teleporter);

    error UnknownRequest(uint256 requestId);
    error UnknownStep(uint256 stepId);
    error RequestNotOpen(uint256 requestId);
    error NotRequester(address caller, address requester);
    error BadSignature(address recovered, address expected);
    error DeadlineExpired(uint256 deadline);
    error NotAssigned(uint256 stepId, address agent);
    error AlreadyResponded(uint256 stepId, address agent);
    error NonceAlreadyUsed(address agent, uint256 nonce);
    error StepClosed(uint256 stepId, uint8 status);
    error BidTooHigh(uint256 bid, uint256 maxPrice);
    error AgentNotEligible(address agent);
    error ReplicationOutOfRange(uint32 replication);
    error FaultToleranceOutOfRange(uint32 f, uint32 replication);
    error DuplicateAssignment(uint256 stepId, address agent);
    error TooManyAssignments(uint256 stepId);
    error NotEnoughResponses(uint256 stepId, uint32 got, uint32 required);
    error StepNotReady(uint256 stepId);
    error NotAllStepsFinal(uint256 requestId, uint256 finalized, uint256 total);
    error OnlySnowball(address caller);
    error OnlyTeleporter(address caller);
    error AlreadySettled(uint256 requestId);
    error ZeroAddress();
    error StepAlreadySettled(uint256 stepId);

    constructor(
        address admin,
        CapabilityRegistry capabilityRegistry,
        ReputationRegistry reputationRegistry,
        StakingSettlement stakingSettlement,
        uint256 protocolFeeBps_
    ) {
        if (admin == address(0)) revert ZeroAddress();
        registry = capabilityRegistry;
        reputation = reputationRegistry;
        settlement = stakingSettlement;
        require(protocolFeeBps_ <= 2000, "fee too high");
        protocolFeeBps = protocolFeeBps_;
        _grantRole(DEFAULT_ADMIN_ROLE, admin);
        _grantRole(COORDINATOR_ADMIN_ROLE, admin);
        _grantRole(COORDINATOR_ROLE, admin);
        _grantRole(PARAMS_ADMIN_ROLE, admin);
    }

    function setSnowball(address s) external onlyRole(COORDINATOR_ADMIN_ROLE) {
        if (s == address(0)) revert ZeroAddress();
        snowball = s;
        emit SnowballSet(s);
    }

    function setTeleporterMessenger(address t) external onlyRole(COORDINATOR_ADMIN_ROLE) {
        if (t == address(0)) revert ZeroAddress();
        teleporterMessenger = t;
        emit TrustedTeleporterSet(t);
    }

    function setProtocolFeeBps(uint256 bps) external onlyRole(PARAMS_ADMIN_ROLE) {
        require(bps <= 2000, "fee too high");
        protocolFeeBps = bps;
        emit ParamsUpdated(bps);
    }

    // ---------------- request lifecycle ----------------

    /// @notice Create a request and escrow its budget. The caller must have approved the settlement contract.
    function createRequest(bytes32 metaHash, uint256 budget, uint8 tier)
        public
        returns (uint256 requestId)
    {
        return _initRequest(msg.sender, msg.sender, metaHash, budget, tier);
    }

    /// @notice Add budget to an existing open request.
    function fundRequest(uint256 requestId, uint256 amount) external nonReentrant {
        Request storage r = _request[requestId];
        if (r.requester == address(0)) revert UnknownRequest(requestId);
        if (r.status != STATUS_OPEN) revert RequestNotOpen(requestId);
        if (msg.sender != r.requester) revert NotRequester(msg.sender, r.requester);
        settlement.depositEscrow(requestId, msg.sender, amount);
        r.budget += amount;
        emit RequestFunded(requestId, msg.sender, amount);
    }

    /// @notice Register one node of the decomposed step graph (proposal 4.2.4).
    function addStep(
        uint256 requestId,
        int256[EMBEDDING_DIM] calldata taskVec,
        uint32 replication,
        uint32 f,
        uint256 maxPrice,
        uint64 deadline,
        bool numeric
    ) external onlyRole(COORDINATOR_ROLE) returns (uint256 stepId) {
        Request storage r = _request[requestId];
        if (r.requester == address(0)) revert UnknownRequest(requestId);
        if (r.status != STATUS_OPEN) revert RequestNotOpen(requestId);
        if (replication == 0 || replication > MAX_COMMITTEE) revert ReplicationOutOfRange(replication);
        if (f > Krum.maxFaults(replication)) revert FaultToleranceOutOfRange(f, replication);
        if (deadline <= block.timestamp) revert DeadlineExpired(deadline);

        stepId = nextStepId++;
        Step storage s = _step[stepId];
        s.requestId = requestId;
        s.taskVec = taskVec;
        s.replication = replication;
        s.f = f;
        s.maxPrice = maxPrice;
        s.deadline = deadline;
        s.status = STEP_PENDING;
        s.numeric = numeric;
        r.stepCount += 1;
        _requestSteps[requestId].push(stepId);
        emit StepAdded(stepId, requestId, replication, f, maxPrice, deadline, numeric);
    }

    /// @notice Assign a step to a candidate agent produced by the off-chain router (4.2.1-4.2.3).
    /// @dev The coordinator chooses who works, but eligibility is enforced on-chain: the agent must
    ///      be registered, active, and hold at least its declared minStake (which is locked).
    function assignAgent(uint256 stepId, address agent, uint256 bid) external onlyRole(COORDINATOR_ROLE) {
        Step storage s = _step[stepId];
        if (s.requestId == 0 && _step[stepId].deadline == 0) revert UnknownStep(stepId);
        if (s.status != STEP_PENDING && s.status != STEP_COLLECTING) revert StepClosed(stepId, s.status);
        if (s.assigned >= s.replication) revert TooManyAssignments(stepId);
        if (_stepAssigned[stepId][agent]) revert DuplicateAssignment(stepId, agent);
        if (bid > s.maxPrice) revert BidTooHigh(bid, s.maxPrice);

        address signer = registry.signerOf(agent);
        if (signer == address(0) || !registry.isActive(agent)) revert AgentNotEligible(agent);

        uint256 requiredStake = registry.minStakeOf(agent);
        uint256 floorStake = settlement.minStake();
        if (requiredStake < floorStake) requiredStake = floorStake;
        uint256 free = settlement.availableStake(agent);
        if (free < requiredStake) revert AgentNotEligible(agent);

        settlement.lockStake(agent, requiredStake);
        _stepAssigned[stepId][agent] = true;
        _stepBid[stepId][agent] = bid;
        _stepAgents[stepId].push(agent);
        s.assigned += 1;
        s.status = STEP_COLLECTING;
        emit AgentAssigned(stepId, agent, bid);
    }

    // ---------------- signed responses ----------------

    /// @notice Submit an agent response. Permissionless: any relayer may deliver the payload, but the
    ///         payload is only accepted if it carries the assigned agent signing key signature.
    function submitResponse(
        uint256 stepId,
        address agent,
        bytes32 outputHash,
        int256 outputValue,
        uint256 nonce,
        uint256 deadline,
        bytes calldata signature
    ) external {
        Step storage s = _step[stepId];
        if (s.status != STEP_COLLECTING && s.status != STEP_PENDING) revert StepClosed(stepId, s.status);
        if (!_stepAssigned[stepId][agent]) revert NotAssigned(stepId, agent);
        if (_stepRespondedFlag[stepId][agent]) revert AlreadyResponded(stepId, agent);
        if (block.timestamp > s.deadline) revert DeadlineExpired(s.deadline);
        if (block.timestamp > deadline) revert DeadlineExpired(deadline);
        if (nonceUsed[agent][nonce]) revert NonceAlreadyUsed(agent, nonce);

        address signer = registry.signerOf(agent);
        address recovered = AttestationLib.recoverResponseSigner(
            _domainSeparator(), s.requestId, stepId, agent, outputHash, outputValue, nonce, deadline, signature
        );
        if (recovered != signer) revert BadSignature(recovered, signer);

        nonceUsed[agent][nonce] = true;
        _stepRespondedFlag[stepId][agent] = true;
        _stepResponses[stepId].push(
            ResponseInternal({
                agent: agent,
                outputHash: outputHash,
                value: outputValue,
                bid: _stepBid[stepId][agent],
                at: uint64(block.timestamp)
            })
        );
        s.responded += 1;
        emit ResponseAccepted(stepId, agent, outputHash, outputValue, nonce);
    }

    // ---------------- aggregation & finalisation ----------------

    /// @notice Aggregate the collected signed responses with Krum (4.2.5) and fix the consensus.
    /// @dev Callable by anyone once every assigned agent has answered or the step deadline passed.
    function finalizeStep(uint256 stepId) external nonReentrant {
        Step storage s = _step[stepId];
        if (s.deadline == 0) revert UnknownStep(stepId);
        if (s.status == STEP_FINALIZED || s.status == STEP_FAILED) revert StepClosed(stepId, s.status);

        uint32 n = s.responded;
        bool allIn = n >= s.assigned;
        if (!allIn && block.timestamp <= s.deadline) revert StepNotReady(stepId);

        if (n == 0) {
            s.status = STEP_FAILED;
            emit StepFailed(stepId, n, s.replication);
            _request[s.requestId].finalizedCount += 1;
            return;
        }

        uint32 required = 3;
        if (s.replication > 1) {
            required = 2 * s.f + 3;
            if (n < required) {
                s.status = STEP_FAILED;
                emit StepFailed(stepId, n, required);
                _request[s.requestId].finalizedCount += 1;
                return;
            }
        }

        ResponseInternal[] storage rs = _stepResponses[stepId];
        int256 winnerValue = 0;
        bytes32 winnerHash = bytes32(0);
        address winnerAgent = address(0);
        uint256 winnerIdx = 0;

        if (s.numeric && n >= 2 * s.f + 3) {
            int256[] memory values = new int256[](n);
            for (uint256 i = 0; i < n; i++) values[i] = rs[i].value;
            (uint256 idx, int256 v, uint256[] memory scores) = Krum.selectConsensus(values, s.f);
            winnerIdx = idx;
            winnerValue = v;
            winnerHash = keccak256(abi.encode(v));
            _stepScores[stepId] = scores;
        } else if (s.numeric) {
            // Committee below the Krum BFT floor (single-agent probe step): nothing to
            // aggregate, so the sole response becomes the consensus value.
            winnerIdx = 0;
            winnerValue = rs[0].value;
            winnerHash = keccak256(abi.encode(winnerValue));
        } else {
            bytes32[] memory hashes = new bytes32[](n);
            for (uint256 i = 0; i < n; i++) hashes[i] = rs[i].outputHash;
            (uint256 idx, bytes32 h, ) = Krum.pluralityHash(hashes);
            winnerIdx = idx;
            winnerHash = h;
        }
        winnerAgent = rs[winnerIdx].agent;
        s.consensusValue = winnerValue;
        s.consensusHash = winnerHash;

        uint256 best1 = type(uint256).max;
        address a1 = address(0);
        uint256 best2 = type(uint256).max;
        address a2 = address(0);
        for (uint256 i = 0; i < n; i++) {
            if (!_matches(s.numeric, rs[i].value, rs[i].outputHash, winnerHash)) continue;
            uint256 b = rs[i].bid;
            if (b < best1) {
                best2 = best1;
                a2 = a1;
                best1 = b;
                a1 = rs[i].agent;
            } else if (b < best2) {
                best2 = b;
                a2 = rs[i].agent;
            }
        }
        s.winner = a1;
        s.runnerUp = a2;
        s.payout = (a2 == address(0)) ? best1 : best2;
        s.status = STEP_FINALIZED;
        _request[s.requestId].finalizedCount += 1;

        emit StepFinalized(stepId, a1, a2, winnerValue, winnerHash, s.payout);
    }

    function _matches(bool numeric, int256 value, bytes32 outputHash, bytes32 consensusHash) internal pure returns (bool) {
        if (numeric) return keccak256(abi.encode(value)) == consensusHash;
        return outputHash == consensusHash;
    }

    mapping(uint256 => uint256[]) private _requestSteps;

    // ---------------- settlement ----------------

    /// @notice Settle every finalized step of a request using the VCG payment fixed at finalisation,
    ///         pay the protocol fee to the treasury, then refund any unused escrow.
    function settleRequest(uint256 requestId) external nonReentrant {
        Request storage r = _request[requestId];
        if (r.requester == address(0)) revert UnknownRequest(requestId);
        if (r.status != STATUS_OPEN) revert AlreadySettled(requestId);
        if (r.finalizedCount < r.stepCount) revert NotAllStepsFinal(requestId, r.finalizedCount, r.stepCount);

        uint256 totalSpent = 0;
        uint256[] storage steps = _requestSteps[requestId];
        for (uint256 k = 0; k < steps.length; k++) {
            uint256 stepId = steps[k];
            Step storage s = _step[stepId];
            address[] storage agents = _stepAgents[stepId];

            if (s.status == STEP_FINALIZED && !s.settled) {
                s.settled = true;
                uint256 avail = settlement.escrowAvailable(requestId);
                uint256 pay = s.payout;
                uint256 fee = (pay * protocolFeeBps) / BPS_DENOMINATOR;
                if (pay + fee > avail) {
                    if (avail > fee) {
                        pay = avail - fee;
                    } else {
                        pay = 0;
                        fee = 0;
                    }
                }
                if (s.winner != address(0) && pay + fee > 0) {
                    settlement.payTask(requestId, s.winner, pay, fee);
                    totalSpent += pay + fee;
                    emit PayoutApplied(requestId, stepId, s.winner, pay, fee);
                }

                ResponseInternal[] storage rs = _stepResponses[stepId];
                for (uint256 i = 0; i < rs.length; i++) {
                    bool matched = _matches(s.numeric, rs[i].value, rs[i].outputHash, s.consensusHash);
                    registry.recordOutcome(rs[i].agent, matched);
                    if (matched) _stepPaid[stepId][rs[i].agent] = true;
                    emit AgentOutcomeRecorded(rs[i].agent, matched);
                }
            }

            for (uint256 i = 0; i < agents.length; i++) {
                settlement.unlockStake(agents[i], _requiredStake(agents[i]));
            }
        }

        r.spent = totalSpent;
        uint256 remaining = settlement.escrowAvailable(requestId);
        r.status = STATUS_SETTLED;
        if (remaining > 0) {
            settlement.refundEscrow(requestId, r.requester, remaining);
        }
        emit RequestSettled(requestId, totalSpent, remaining);
    }

    function _requiredStake(address agent) internal view returns (uint256) {
        uint256 rs = registry.minStakeOf(agent);
        uint256 floorStake = settlement.minStake();
        return rs < floorStake ? floorStake : rs;
    }

    function _domainSeparator() internal view returns (bytes32) {
        return AttestationLib.domainSeparator(block.chainid, address(this));
    }

    /// @notice Shared request-creation path used by direct and interchain (ICM) callers.
    function _initRequest(address requester, address payer, bytes32 metaHash, uint256 budget, uint8 tier)
        internal
        nonReentrant
        returns (uint256 requestId)
    {
        require(tier <= TIER_ZKML_PROOF, "bad tier");
        if (requester == address(0) || payer == address(0)) revert ZeroAddress();
        requestId = nextRequestId++;
        Request storage r = _request[requestId];
        r.requester = requester;
        r.metaHash = metaHash;
        r.budget = budget;
        r.createdAt = uint64(block.timestamp);
        r.status = STATUS_OPEN;
        r.tier = tier;
        if (budget > 0) {
            settlement.depositEscrow(requestId, payer, budget);
        }
        emit RequestCreated(requestId, requester, metaHash, budget, tier);
        emit RequestFunded(requestId, payer, budget);
    }

    // ---------------- dispute hooks (Hive Snowball) ----------------

    /// @notice Called by the Hive Snowball dispute contract once a challenge has been resolved.
    /// @dev If the dispute upholds the challenger the step result is invalidated: the winner is not
    ///      paid and the requester keeps the escrow. Slashing itself happens in StakingSettlement.
    function applyDisputeOutcome(uint256 stepId, bool winnerWrong) external onlyRole(SNOWBALL_ROLE) {
        Step storage s = _step[stepId];
        if (s.deadline == 0) revert UnknownStep(stepId);
        if (winnerWrong) {
            s.status = STEP_FAILED;
            s.payout = 0;
        }
        emit DisputeOutcomeApplied(stepId, s.winner, winnerWrong);
    }

    // ---------------- Avalanche Interchain Messaging (ICM) ----------------

    /// @notice ICM entrypoint. Avalanche IPs/L1s send requests into the C-Chain through the
    ///         Teleporter messenger; the C-Chain contracts answer back the same way. No bridge.
    /// @dev Message payload is abi.encode(bytes32 metaHash, uint256 budget, uint8 tier, address payer).
    ///      `payer` is a C-Chain address that pre-approved StakingSettlement for the budget.
    function receiveTeleporterMessage(
        bytes32 sourceBlockchainID,
        address originSenderAddress,
        bytes calldata message
    ) external {
        if (msg.sender != teleporterMessenger) revert OnlyTeleporter(msg.sender);
        (bytes32 metaHash, uint256 budget, uint8 tier, address payer) =
            abi.decode(message, (bytes32, uint256, uint8, address));
        uint256 requestId = _initRequest(originSenderAddress, payer, metaHash, budget, tier);
        Request storage r = _request[requestId];
        r.sourceBlockchainID = sourceBlockchainID;
        r.originSender = originSenderAddress;
        emit InterchainRequestReceived(sourceBlockchainID, originSenderAddress, requestId, budget);
    }

    /// @notice Send the aggregated answer for a request back to the requesting chain via ICM.
    function sendInterchainAnswer(bytes32 destinationBlockchainID, address destination, uint256 requestId)
        external
        onlyRole(COORDINATOR_ROLE)
    {
        Request storage r = _request[requestId];
        if (r.requester == address(0)) revert UnknownRequest(requestId);
        bytes32 aHash = _answerHash(requestId);
        emit InterchainAnswerSent(destinationBlockchainID, destination, requestId, aHash);
        if (teleporterMessenger != address(0)) {
            ITeleporterMessenger(teleporterMessenger).sendCrossChainAnswer(
                destinationBlockchainID, destination, requestId, aHash
            );
        }
    }

    // ---------------- views ----------------

    /// @notice Aggregate answer for a request: hash over every step consensus (used by ICM answers).
    function answerHash(uint256 requestId) external view returns (bytes32) {
        return _answerHash(requestId);
    }

    function _answerHash(uint256 requestId) internal view returns (bytes32) {
        uint256[] storage steps = _requestSteps[requestId];
        bytes32 acc = keccak256(abi.encode(requestId, steps.length));
        for (uint256 i = 0; i < steps.length; i++) {
            acc = keccak256(abi.encode(acc, steps[i], _step[steps[i]].consensusHash));
        }
        return acc;
    }

    function requestInfo(uint256 requestId)
        external
        view
        returns (
            address requester,
            bytes32 metaHash,
            uint256 budget,
            uint256 spent,
            uint8 status,
            uint8 tier,
            uint256 stepCount,
            uint256 finalizedCount
        )
    {
        Request storage r = _request[requestId];
        return (r.requester, r.metaHash, r.budget, r.spent, r.status, r.tier, r.stepCount, r.finalizedCount);
    }

    function stepInfo(uint256 stepId) external view returns (Step memory) {
        return _step[stepId];
    }

    function stepAgentCount(uint256 stepId) external view returns (uint256) {
        return _stepAgents[stepId].length;
    }

    function stepAgentAt(uint256 stepId, uint256 i) external view returns (address) {
        return _stepAgents[stepId][i];
    }

    function bidOf(uint256 stepId, address agent) external view returns (uint256) {
        return _stepBid[stepId][agent];
    }

    function isAssigned(uint256 stepId, address agent) external view returns (bool) {
        return _stepAssigned[stepId][agent];
    }

    function isPaid(uint256 stepId, address agent) external view returns (bool) {
        return _stepPaid[stepId][agent];
    }

    function responseCount(uint256 stepId) external view returns (uint256) {
        return _stepResponses[stepId].length;
    }

    function responseAt(uint256 stepId, uint256 i) external view returns (ResponseView memory) {
        ResponseInternal storage r = _stepResponses[stepId][i];
        Step storage s = _step[stepId];
        return ResponseView({
            agent: r.agent,
            outputHash: r.outputHash,
            value: r.value,
            bid: r.bid,
            at: r.at,
            matchesConsensus: _matches(s.numeric, r.value, r.outputHash, s.consensusHash),
            paid: _stepPaid[stepId][r.agent]
        });
    }

    function consensusScoreCount(uint256 stepId) external view returns (uint256) {
        return _stepScores[stepId].length;
    }

    function consensusScoreAt(uint256 stepId, uint256 i) external view returns (uint256) {
        return _stepScores[stepId][i];
    }

    function isNonceUsed(address agent, uint256 nonce) external view returns (bool) {
        return nonceUsed[agent][nonce];
    }
}
