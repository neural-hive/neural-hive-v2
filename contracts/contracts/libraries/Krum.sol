// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/// @title Krum
/// @notice On-chain implementation of the Krum aggregation rule
///         (Blanchard, El Mhamdi, Guerraoui, Stainer, NeurIPS 2017), proposal section 4.2.5.
/// @dev Krum scores each output by the sum of its (n - f - 2) smallest distances to the other
///      outputs and returns the output with the smallest score. This is the exact rule in the
///      proposal pseudocode, so anyone can re-run the aggregation on-chain from the signed
///      responses and check that the protocol did not pick a self-serving answer.
library Krum {
    error NotEnoughResponses(uint256 n, uint256 f);

    /// @notice Maximum tolerable Byzantine responses for a committee of size n.
    /// @dev BFT requires n >= 2f + 3.
    function maxFaults(uint256 n) internal pure returns (uint256) {
        if (n < 3) return 0;
        return (n - 3) / 2;
    }

    /// @dev |a - b| as an unsigned magnitude, safe against int256 overflow.
    function absDiff(int256 a, int256 b) internal pure returns (uint256) {
        if (a >= b) {
            return uint256(a - b);
        }
        return uint256(b - a);
    }

    /// @dev Ascending insertion sort. n is small (committee size), so O(n^2) is optimal in practice.
    function sortAsc(uint256[] memory arr) internal pure {
        for (uint256 i = 1; i < arr.length; i++) {
            uint256 key = arr[i];
            uint256 j = i;
            while (j > 0 && arr[j - 1] > key) {
                arr[j] = arr[j - 1];
                j--;
            }
            arr[j] = key;
        }
    }

    /// @notice Run Krum over numeric outputs.
    /// @param values Numeric outputs reported by the committee (one per responder).
    /// @param f Byzantine tolerance parameter; must satisfy n >= 2f + 3.
    /// @return winnerIndex Index in `values` of the Krum-selected output.
    /// @return winnerValue The selected numeric output.
    /// @return scores The per-index Krum scores (exposed for off-chain auditing).
    function selectConsensus(int256[] memory values, uint256 f)
        internal
        pure
        returns (uint256 winnerIndex, int256 winnerValue, uint256[] memory scores)
    {
        uint256 n = values.length;
        if (n < 2 * f + 3) revert NotEnoughResponses(n, f);
        uint256 keep = n - f - 2; // number of smallest distances summed per candidate
        scores = new uint256[](n);
        uint256[64] memory buf;

        for (uint256 i = 0; i < n; i++) {
            uint256 m = 0;
            for (uint256 j = 0; j < n; j++) {
                if (j == i) continue;
                buf[m] = absDiff(values[i], values[j]);
                m++;
            }
            // sort the first m entries ascending
            for (uint256 a = 1; a < m; a++) {
                uint256 key = buf[a];
                uint256 b = a;
                while (b > 0 && buf[b - 1] > key) {
                    buf[b] = buf[b - 1];
                    b--;
                }
                buf[b] = key;
            }
            uint256 s = 0;
            for (uint256 a = 0; a < keep; a++) {
                s += buf[a];
            }
            scores[i] = s;
        }

        uint256 best = 0;
        for (uint256 i = 1; i < n; i++) {
            if (scores[i] < scores[best]) best = i;
        }
        return (best, values[best], scores);
    }

    /// @notice Plurality rule over hashed (text/structured) outputs, used when a step has no
    ///         numeric value to compute distances over.
    function pluralityHash(bytes32[] memory hashes) internal pure returns (uint256 winnerIndex, bytes32 winner, uint256 count) {
        uint256 n = hashes.length;
        for (uint256 i = 0; i < n; i++) {
            uint256 c = 0;
            for (uint256 j = 0; j < n; j++) {
                if (hashes[i] == hashes[j]) c++;
            }
            if (c > count) {
                count = c;
                winner = hashes[i];
                winnerIndex = i;
            }
        }
    }
}
