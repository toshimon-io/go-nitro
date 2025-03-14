// SPDX-License-Identifier: MIT
pragma solidity 0.7.6;
pragma experimental ABIEncoderV2;

import {ExitFormat as Outcome} from '@statechannels/exit-format/contracts/ExitFormat.sol';
import {NitroUtils} from '../NitroUtils.sol';
import '../../interfaces/INitroTypes.sol';

library StrictTurnTaking {
    /**
     * @notice Require supplied arguments to comply with turn taking logic, i.e. each participant signed the one state, they were mover for.
     * @dev Require supplied arguments to comply with turn taking logic, i.e. each participant signed the one state, they were mover for.
     * @param fixedPart Data describing properties of the state channel that do not change with state updates.
     * @param signedVariableParts An ordered array of structs, each struct describing dynamic properties of the state channel and must be signed by corresponding moving participant.
     */
    function requireValidTurnTaking(
        INitroTypes.FixedPart memory fixedPart,
        INitroTypes.SignedVariablePart[] memory signedVariableParts
    ) internal pure {
        _requireValidInput(fixedPart.participants.length, signedVariableParts.length);
        
        uint48 turnNum = signedVariableParts[0].variablePart.turnNum;

        for (uint i = 0; i < signedVariableParts.length; i++) {
            isSignedByMover(fixedPart, signedVariableParts[i]);
            requireHasTurnNum(signedVariableParts[i].variablePart, turnNum);
            turnNum++;
        }
    }

    /**
     * @notice Require supplied state is signed by its corresponding moving participant.
     * @dev Require supplied state is signed by its corresponding moving participant.
     * @param fixedPart Data describing properties of the state channel that do not change with state updates.
     * @param signedVariablePart A struct describing dynamic properties of the state channel, that must be signed by moving participant.
     */
    function isSignedByMover(
        INitroTypes.FixedPart memory fixedPart,
        INitroTypes.SignedVariablePart memory signedVariablePart
    ) internal pure {
        require(signedVariablePart.sigs.length == 1, 'sigs.length != 1');
        require(
            NitroUtils.isClaimedSignedOnlyBy(signedVariablePart.signedBy, uint8(signedVariablePart.variablePart.turnNum % fixedPart.participants.length)),
            'Invalid signedBy'
        );

        require(
            NitroUtils.isSignedBy(
                NitroUtils.hashState(
                    NitroUtils.getChannelId(fixedPart),
                    signedVariablePart.variablePart.appData,
                    signedVariablePart.variablePart.outcome,
                    signedVariablePart.variablePart.turnNum,
                    signedVariablePart.variablePart.isFinal
                ),
                signedVariablePart.sigs[0],
                _moverAddress(fixedPart.participants, signedVariablePart.variablePart.turnNum)
            ),
            'Invalid signer'
        );
    }

    /**
     * @notice Require supplied variable part has specified turn number.    
     * @dev Require supplied variable part has specified turn number.
     * @param variablePart Variable part to check turn number of.
     * @param turnNum Turn number to compare with.
     */
    function requireHasTurnNum(
        INitroTypes.VariablePart memory variablePart,
        uint48 turnNum
    ) internal pure {
        require(
            variablePart.turnNum == turnNum,
            'Wrong variablePart.turnNum'
        );
    }

    /**
     * @notice Find moving participant address based on state turn number.
     * @dev Find moving participant address based on state turn number.
     * @param participants Array of participant addresses.
     * @param turnNum State turn number.
     * @return address Moving partitipant address.
     */
    function _moverAddress(
        address[] memory participants,
        uint48 turnNum
    ) internal pure returns (address) {
        return participants[turnNum % participants.length];
    }

    /**
     * @notice Validate input for turn taking logic.
     * @dev Validate input for turn taking logic.
     * @param numParticipants Number of participants in a channel.
     * @param numStates Number of states submitted.
     */
    function _requireValidInput(
        uint256 numParticipants,
        uint256 numStates
    ) internal pure {
        require((numParticipants >= numStates) && (numStates > 0), 'Insufficient or excess states');

        // no more than 255 participants
        require(numParticipants <= type(uint8).max, 'Too many participants'); // type(uint8).max = 2**8 - 1 = 255
    }
}
