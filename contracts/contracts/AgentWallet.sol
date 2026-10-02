// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Ownable} from "@openzeppelin/contracts/access/Ownable.sol";

/// @title AgentWallet
/// @notice Per-agent vault that holds the HIVE an agent earns. One smart contract is deployed
///         per agent. Only the agent owner can withdraw the balance; nobody else can move funds.
contract AgentWallet is Ownable {
    using SafeERC20 for IERC20;

    IERC20 public immutable hive;
    string public agentId;

    event Deposited(address indexed from, uint256 amount);
    event Withdrawn(address indexed to, uint256 amount);

    constructor(address owner_, address hive_, string memory agentId_) {
        require(owner_ != address(0), "owner=0");
        hive = IERC20(hive_);
        _transferOwnership(owner_);
        agentId = agentId_;
    }

    /// @notice Records a HIVE deposit (the Hub settlement transfers HIVE here).
    function deposit(uint256 amount) external {
        if (amount > 0) {
            hive.safeTransferFrom(msg.sender, address(this), amount);
        }
        emit Deposited(msg.sender, amount);
    }

    /// @notice Withdraws the full HIVE balance to the owner. Owner only.
    function withdraw() external onlyOwner returns (uint256 amount) {
        amount = hive.balanceOf(address(this));
        require(amount > 0, "nothing to withdraw");
        hive.safeTransfer(owner(), amount);
        emit Withdrawn(owner(), amount);
    }

    /// @notice Withdraws the full HIVE balance to a chosen address. Owner only.
    function withdrawTo(address to) external onlyOwner returns (uint256 amount) {
        require(to != address(0), "to=0");
        amount = hive.balanceOf(address(this));
        require(amount > 0, "nothing to withdraw");
        hive.safeTransfer(to, amount);
        emit Withdrawn(to, amount);
    }

    /// @notice The HIVE balance currently held by this agent vault.
    function balance() external view returns (uint256) {
        return hive.balanceOf(address(this));
    }
}
