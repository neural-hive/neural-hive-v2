// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/security/ReentrancyGuard.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {HiveToken} from "./HiveToken.sol";

/// @title StakingSettlement
/// @notice Custody of agent stake and of per-request escrow, plus the settlement primitive that
///         pays agents according to the VCG rule computed by the TaskCoordinator (proposal 4.2.7)
///         and the slashing primitive used when a challenge proves an agent wrong (4.2.9).
/// @dev Stake is bounded: a stake is only slashable up to what has been deposited, which makes
///      Sybil attacks expensive. Locked stake cannot be withdrawn while a task is in flight.
contract StakingSettlement is AccessControl, ReentrancyGuard {
    using SafeERC20 for HiveToken;

    bytes32 public constant SETTLEMENT_ADMIN_ROLE = keccak256("SETTLEMENT_ADMIN_ROLE");
    bytes32 public constant SETTLEMENT_ROLE = keccak256("SETTLEMENT_ROLE");
    bytes32 public constant SLASHER_ROLE = keccak256("SLASHER_ROLE");
    bytes32 public constant TREASURY_ADMIN_ROLE = keccak256("TREASURY_ADMIN_ROLE");

    HiveToken public immutable hive;

    address public treasury;
    uint256 public unstakeCooldown = 1 days;
    uint256 public minStake = 1e18; // 1 HIVE minimum to participate

    mapping(address => uint256) public stakeOf;
    mapping(address => uint256) public lockedOf;
    mapping(address => uint256) public pendingUnstake;
    mapping(address => uint256) public unstakeReadyAt;
    mapping(uint256 => uint256) public escrowOf;
    mapping(address => uint256) public slashedTotal;
    mapping(address => uint256) public earnedTotal;

    uint256 public totalStaked;
    uint256 public totalEscrowed;
    uint256 public totalProtocolFees;

    event Staked(address indexed agent, uint256 amount, uint256 newStake);
    event UnstakeRequested(address indexed agent, uint256 amount, uint256 readyAt);
    event UnstakeCancelled(address indexed agent, uint256 amount);
    event Withdrawn(address indexed agent, uint256 amount);
    event StakeLocked(address indexed agent, uint256 amount);
    event StakeUnlocked(address indexed agent, uint256 amount);
    event EscrowDeposited(uint256 indexed requestId, address indexed payer, uint256 amount);
    event EscrowRefunded(uint256 indexed requestId, address indexed to, uint256 amount);
    event TaskPaid(uint256 indexed requestId, address indexed payee, uint256 payment, uint256 protocolFee);
    event AgentSlashed(address indexed agent, uint256 amount, address indexed beneficiary, bytes32 reason);
    event TreasuryUpdated(address treasury);
    event MinStakeUpdated(uint256 minStake);
    event CooldownUpdated(uint256 cooldown);

    error ZeroAddress();
    error InsufficientStake(address agent, uint256 available, uint256 required);
    error InsufficientUnlocked(address agent, uint256 unlocked, uint256 required);
    error InsufficientEscrow(uint256 requestId, uint256 available, uint256 required);
    error CooldownActive(uint256 readyAt);
    error NothingToWithdraw();
    error BelowMinStake(uint256 amount, uint256 minStake);

    constructor(address admin, HiveToken hiveToken, address treasury_) {
        require(admin != address(0), "admin=0");
        if (address(hiveToken) == address(0)) revert ZeroAddress();
        if (treasury_ == address(0)) revert ZeroAddress();
        hive = hiveToken;
        treasury = treasury_;
        _grantRole(DEFAULT_ADMIN_ROLE, admin);
        _grantRole(SETTLEMENT_ADMIN_ROLE, admin);
        _grantRole(SETTLEMENT_ROLE, admin);
        _grantRole(SLASHER_ROLE, admin);
        _grantRole(TREASURY_ADMIN_ROLE, admin);
    }

    // ---------------- staking ----------------

    /// @notice Deposit HIVE as slashable stake backing this agent identity.
    function stake(uint256 amount) external nonReentrant {
        if (amount == 0) revert BelowMinStake(amount, minStake);
        hive.safeTransferFrom(msg.sender, address(this), amount);
        stakeOf[msg.sender] += amount;
        totalStaked += amount;
        emit Staked(msg.sender, amount, stakeOf[msg.sender]);
    }

    /// @notice Begin unbonding. Stake stays slashable during the cooldown.
    function requestUnstake(uint256 amount) external {
        uint256 unlocked = stakeOf[msg.sender] - lockedOf[msg.sender];
        if (amount > unlocked) revert InsufficientUnlocked(msg.sender, unlocked, amount);
        if (stakeOf[msg.sender] - amount < minStake && stakeOf[msg.sender] - amount != 0) {
            revert BelowMinStake(stakeOf[msg.sender] - amount, minStake);
        }
        pendingUnstake[msg.sender] += amount;
        unstakeReadyAt[msg.sender] = block.timestamp + unstakeCooldown;
        emit UnstakeRequested(msg.sender, amount, unstakeReadyAt[msg.sender]);
    }

    function cancelUnstake() external {
        uint256 amount = pendingUnstake[msg.sender];
        pendingUnstake[msg.sender] = 0;
        unstakeReadyAt[msg.sender] = 0;
        emit UnstakeCancelled(msg.sender, amount);
    }

    function withdraw() external nonReentrant {
        uint256 amount = pendingUnstake[msg.sender];
        if (amount == 0) revert NothingToWithdraw();
        if (block.timestamp < unstakeReadyAt[msg.sender]) revert CooldownActive(unstakeReadyAt[msg.sender]);
        uint256 unlocked = stakeOf[msg.sender] - lockedOf[msg.sender];
        if (amount > unlocked) revert InsufficientUnlocked(msg.sender, unlocked, amount);
        pendingUnstake[msg.sender] = 0;
        unstakeReadyAt[msg.sender] = 0;
        stakeOf[msg.sender] -= amount;
        totalStaked -= amount;
        hive.safeTransfer(msg.sender, amount);
        emit Withdrawn(msg.sender, amount);
    }

    /// @notice Lock stake for the duration of an assignment so it cannot be exited mid-task.
    function lockStake(address agent, uint256 amount) external onlyRole(SETTLEMENT_ROLE) {
        uint256 free = stakeOf[agent] - lockedOf[agent];
        if (amount > free) revert InsufficientStake(agent, free, amount);
        lockedOf[agent] += amount;
        emit StakeLocked(agent, amount);
    }

    function unlockStake(address agent, uint256 amount) external onlyRole(SETTLEMENT_ROLE) {
        uint256 l = lockedOf[agent];
        uint256 release = amount > l ? l : amount;
        lockedOf[agent] = l - release;
        emit StakeUnlocked(agent, release);
    }

    // ---------------- escrow ----------------

    /// @notice Lock a requester budget for a request. The payer must have approved this contract.
    function depositEscrow(uint256 requestId, address payer, uint256 amount)
        external
        onlyRole(SETTLEMENT_ROLE)
        nonReentrant
    {
        if (payer == address(0)) revert ZeroAddress();
        hive.safeTransferFrom(payer, address(this), amount);
        escrowOf[requestId] += amount;
        totalEscrowed += amount;
        emit EscrowDeposited(requestId, payer, amount);
    }

    /// @notice Refund unused escrow back to the requester (or any beneficiary the coordinator names).
    function refundEscrow(uint256 requestId, address to, uint256 amount)
        external
        onlyRole(SETTLEMENT_ROLE)
        nonReentrant
    {
        uint256 available = escrowOf[requestId];
        if (amount > available) revert InsufficientEscrow(requestId, available, amount);
        escrowOf[requestId] = available - amount;
        totalEscrowed -= amount;
        hive.safeTransfer(to, amount);
        emit EscrowRefunded(requestId, to, amount);
    }

    /// @notice Settle one task: pay the agent the VCG-cleared amount and the treasury the protocol fee.
    /// @dev VCG payment itself is computed in TaskCoordinator; this contract only moves funds, so the
    ///      money path stays small and auditable.
    function payTask(uint256 requestId, address payee, uint256 payment, uint256 protocolFee)
        external
        onlyRole(SETTLEMENT_ROLE)
        nonReentrant
    {
        uint256 required = payment + protocolFee;
        uint256 available = escrowOf[requestId];
        if (required > available) revert InsufficientEscrow(requestId, available, required);
        escrowOf[requestId] = available - required;
        totalEscrowed -= required;
        if (payment > 0) {
            earnedTotal[payee] += payment;
            hive.safeTransfer(payee, payment);
        }
        if (protocolFee > 0) {
            totalProtocolFees += protocolFee;
            hive.safeTransfer(treasury, protocolFee);
        }
        emit TaskPaid(requestId, payee, payment, protocolFee);
    }

    // ---------------- slashing ----------------

    /// @notice Seize stake from an agent proven wrong and pay it to a beneficiary.
    function slash(address agent, uint256 amount, address beneficiary, bytes32 reason)
        external
        onlyRole(SLASHER_ROLE)
        nonReentrant
    {
        if (beneficiary == address(0)) revert ZeroAddress();
        uint256 available = stakeOf[agent];
        uint256 take = amount > available ? available : amount;
        stakeOf[agent] = available - take;
        totalStaked -= take;
        slashedTotal[agent] += take;
        if (lockedOf[agent] > stakeOf[agent]) lockedOf[agent] = stakeOf[agent];
        if (take > 0) hive.safeTransfer(beneficiary, take);
        emit AgentSlashed(agent, take, beneficiary, reason);
    }

    // ---------------- admin ----------------

    function setTreasury(address t) external onlyRole(TREASURY_ADMIN_ROLE) {
        if (t == address(0)) revert ZeroAddress();
        treasury = t;
        emit TreasuryUpdated(t);
    }

    function setMinStake(uint256 m) external onlyRole(SETTLEMENT_ADMIN_ROLE) {
        minStake = m;
        emit MinStakeUpdated(m);
    }

    function setUnstakeCooldown(uint256 c) external onlyRole(SETTLEMENT_ADMIN_ROLE) {
        unstakeCooldown = c;
        emit CooldownUpdated(c);
    }

    // ---------------- views ----------------

    function availableStake(address agent) external view returns (uint256) {
        return stakeOf[agent] - lockedOf[agent];
    }

    function escrowAvailable(uint256 requestId) external view returns (uint256) {
        return escrowOf[requestId];
    }
}
