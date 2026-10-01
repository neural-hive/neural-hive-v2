// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {ERC20} from "@openzeppelin/contracts/token/ERC20/ERC20.sol";
import {AccessControl} from "@openzeppelin/contracts/access/AccessControl.sol";

/// @title HIVE
/// @notice Utility token used to pay for Neural Hive tasks, to stake agents, and to reward
///         verified work. Deployed as a standard ERC-20 on the C-Chain; it is NOT a native gas token.
/// @dev Supply is capped. Minting is reserved for the rewards module (MINTER_ROLE). Burning is used
///      to remove slashed stake from circulation.
contract HiveToken is ERC20, AccessControl {
    bytes32 public constant MINTER_ROLE = keccak256("MINTER_ROLE");

    /// @dev Illustrative cap (1e9 HIVE). The proposal tokenomics are a starting point, not final.
    uint256 public constant MAX_SUPPLY = 1_000_000_000 ether;

    event Minted(address indexed to, uint256 amount);
    event Burned(address indexed from, uint256 amount);

    error MaxSupplyExceeded(uint256 requested, uint256 remaining);

    constructor(address admin, uint256 initialSupply) ERC20("Neural Hive", "HIVE") {
        require(admin != address(0), "admin=0");
        _grantRole(DEFAULT_ADMIN_ROLE, admin);
        _grantRole(MINTER_ROLE, admin);
        if (initialSupply > 0) {
            _mint(admin, initialSupply);
            emit Minted(admin, initialSupply);
        }
    }

    /// @notice Mint new HIVE (agent/compute rewards).
    function mint(address to, uint256 amount) external onlyRole(MINTER_ROLE) {
        uint256 remaining = MAX_SUPPLY - totalSupply();
        if (amount > remaining) revert MaxSupplyExceeded(amount, remaining);
        _mint(to, amount);
        emit Minted(to, amount);
    }

    /// @notice Burn HIVE from the caller. Slashed stake is burned by the settlement contract.
    function burn(uint256 amount) external {
        _burn(msg.sender, amount);
        emit Burned(msg.sender, amount);
    }
}
