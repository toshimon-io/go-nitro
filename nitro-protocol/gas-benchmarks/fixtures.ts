import {BigNumber, BigNumberish, constants, ContractReceipt, ethers} from 'ethers';
import {Signature} from '@ethersproject/bytes';
import {Wallet} from '@ethersproject/wallet';
import {AllocationType} from '@statechannels/exit-format';

import {
  convertAddressToBytes32,
  getChannelId,
  getFixedPart,
  signChallengeMessage,
  SignedState,
  signState,
  State,
} from '../src';
import {
  encodeGuaranteeData,
  GuaranteeAllocation,
  Outcome,
  SimpleAllocation,
} from '../src/contract/outcome';
import {FixedPart, getVariablePart, hashState, SignedVariablePart} from '../src/contract/state';

import {nitroAdjudicator, provider, trivialAppAddress} from './vanillaSetup';

export const chainId = '0x7a69'; // 31337 in hex (hardhat network default)

export const Alice = new Wallet(
  '0x277fb9e0ad81dc836c60294e385b10dfcc0a9586eeb0b1d31da92e384a0d2efa'
);
export const Bob = new Wallet('0xc8774aa98410b3e3281ff1ec40ea2637d2b9280328c4d1ff00d06cd95dd42cbd');
export const Ingrid = new Wallet(
  '0x558789345da13a7ac1d6d6ac9275ba66836eb4a088efc1920db0f5d092d6ee71'
);
export const participants = [Alice.address, Bob.address];

export const amountForAlice = BigNumber.from(5).toHexString();
export const amountForBob = BigNumber.from(5).toHexString();
export const amountForAliceAndBob = BigNumber.from(amountForAlice).add(amountForBob).toHexString();

class TestChannel {
  constructor(
    channelNonce: number,
    wallets: ethers.Wallet[],
    allocations: Array<GuaranteeAllocation | SimpleAllocation>
  ) {
    this.wallets = wallets;
    this.fixedPart = {
      chainId,
      channelNonce,
      participants: wallets.map(w => w.address),
      appDefinition: trivialAppAddress, // TODO adjust this to point to an appropriate (deployed) application, according to the channel type
      challengeDuration: 600,
    };
    this.allocations = allocations;
  }
  wallets: ethers.Wallet[];
  fixedPart: FixedPart;
  private allocations: Array<GuaranteeAllocation | SimpleAllocation>;
  outcome(asset: string) {
    const outcome: Outcome = [{asset, allocations: this.allocations, metadata: '0x'}];
    return outcome;
  }
  get channelId() {
    return getChannelId(this.fixedPart);
  }
  someState(asset: string): State {
    return {
      challengeDuration: 600,
      appDefinition: trivialAppAddress, // TODO adjust this to point to an appropriate (deployed) application, according to the channel type
      channel: this.fixedPart,
      turnNum: 6,
      isFinal: false,
      outcome: this.outcome(asset),
      appData: '0x', // TODO choose a more representative example
    };
  }

  finalState(asset: string): State {
    return {
      ...this.someState(asset),
      isFinal: true,
    };
  }

  counterSignedSupportProof(
    // for challenging and outcome pushing
    state: State
  ): {
    fixedPart: FixedPart;
    signedVariableParts: SignedVariablePart[];
    challengeSignature: Signature;
    outcome: Outcome;
    stateHash: string;
  } {
    return {
      fixedPart: getFixedPart(state),
      signedVariableParts: [
        {
          variablePart: getVariablePart(state),
          sigs: this.wallets.map(w => signState(state, w.privateKey).signature),
          signedBy: '0',
        },
      ],
      challengeSignature: signChallengeMessage([{state} as SignedState], Alice.privateKey),
      outcome: state.outcome,
      stateHash: hashState(state),
    };
  }

  supportProof(
    // for concluding
    state: State
  ): {
    fixedPart: FixedPart;
    signedVariableParts: SignedVariablePart[];
  } {
    return {
      fixedPart: getFixedPart(state),
      signedVariableParts: [
        {
          variablePart: getVariablePart(state),
          sigs: this.wallets.map(w => signState(state, w.privateKey).signature),
          signedBy: '0',
        },
      ],
    };
  }

  async concludeAndTransferAllAssetsTx(asset: string) {
    const fP = this.supportProof(this.finalState(asset));
    return await nitroAdjudicator.concludeAndTransferAllAssets(
      fP.fixedPart,
      fP.signedVariableParts
    );
  }

  async challengeTx(asset: string) {
    const proof = this.counterSignedSupportProof(this.someState(asset));
    return await nitroAdjudicator.challenge(
      proof.fixedPart,
      proof.signedVariableParts,
      proof.challengeSignature
    );
  }
}

/** An application channel between Alice and Bob */
export const X = new TestChannel(
  2,
  [Alice, Bob],
  [
    {
      destination: convertAddressToBytes32(Alice.address),
      amount: amountForAlice,
      metadata: '0x',
      allocationType: AllocationType.simple,
    },
    {
      destination: convertAddressToBytes32(Bob.address),
      amount: amountForBob,
      metadata: '0x',
      allocationType: AllocationType.simple,
    },
  ]
);

/** Another application channel between Alice and Bob */
export const Y = new TestChannel(
  3,
  [Alice, Bob],
  [
    {
      destination: convertAddressToBytes32(Alice.address),
      amount: amountForAlice,
      metadata: '0x',
      allocationType: AllocationType.simple,
    },
    {
      destination: convertAddressToBytes32(Bob.address),
      amount: amountForBob,
      metadata: '0x',
      allocationType: AllocationType.simple,
    },
  ]
);

/** Ledger channel between Alice and Bob, providing funds to channel X */
export const LforX = new TestChannel(
  4,
  [Alice, Bob],
  [
    {
      destination: X.channelId,
      amount: amountForAliceAndBob,
      metadata: '0x',
      allocationType: AllocationType.simple,
    },
  ]
);

/** Joint channel between Alice, Bob, and Ingrid, funding application channel X */
export const J = new TestChannel(
  5,
  [Alice, Bob, Ingrid],
  [
    {
      destination: X.channelId,
      amount: amountForAliceAndBob,
      metadata: '0x',
      allocationType: AllocationType.simple,
    },
    {
      destination: convertAddressToBytes32(Ingrid.address),
      amount: amountForAliceAndBob,
      metadata: '0x',
      allocationType: AllocationType.simple,
    },
  ]
);

/** Ledger channel between Alice and Ingid, with Guarantee targeting joint channel J */

export const LforJ = new TestChannel(
  7,
  [Alice, Bob],
  [
    {
      destination: J.channelId,
      amount: amountForAliceAndBob,
      metadata: encodeGuaranteeData([
        X.channelId,
        convertAddressToBytes32(Alice.address),
        convertAddressToBytes32(Ingrid.address),
      ]),
      allocationType: AllocationType.guarantee,
    },
  ]
);

// Utils
export async function getFinalizesAtFromTransactionHash(hash: string): Promise<number> {
  const receipt = (await provider.getTransactionReceipt(hash)) as ContractReceipt;
  return nitroAdjudicator.interface.decodeEventLog('ChallengeRegistered', receipt.logs[0].data)[2];
}
export async function waitForChallengesToTimeOut(finalizesAtArray: number[]): Promise<void> {
  const finalizesAt = Math.max(...finalizesAtArray);
  await provider.send('evm_setNextBlockTimestamp', [finalizesAt + 1]);
  await provider.send('evm_mine', []);
}

/**
 * Constructs a support proof for the supplied channel, calls challenge,
 * and asserts the expected gas
 * @returns The proof and finalizesAt
 */
export async function challengeChannelAndExpectGas(
  channel: TestChannel,
  asset: string,
  expectedGas: number
): Promise<{proof: ReturnType<typeof channel.counterSignedSupportProof>; finalizesAt: number}> {
  const proof = channel.counterSignedSupportProof(channel.someState(asset)); // TODO use a nontrivial app with a state transition

  const challengeTx = await nitroAdjudicator.challenge(
    proof.fixedPart,
    proof.signedVariableParts,
    proof.challengeSignature
  );
  await expect(challengeTx).toConsumeGas(expectedGas);

  const finalizesAt = await getFinalizesAtFromTransactionHash(challengeTx.hash);
  return {proof, finalizesAt};
}

interface ETHBalances {
  Alice: BigNumberish;
  Bob: BigNumberish;
  Ingrid: BigNumberish;
}

interface ETHHoldings {
  LforJ: BigNumberish;
  J: BigNumberish;
  X: BigNumberish;
}

/**
 * Asserts the ETH balance of the supplied ethereum account addresses and the ETH holdings in the statechannels asset holding contract for the supplied channelIds.
 */
export async function assertEthBalancesAndHoldings(
  ethBalances: Partial<ETHBalances>,
  ethHoldings: Partial<ETHHoldings>
): Promise<void> {
  const internalDestinations: {[Property in keyof ETHHoldings]: string} = {
    LforJ: LforJ.channelId,
    J: J.channelId,
    X: X.channelId,
  };
  const externalDestinations: {[Property in keyof ETHBalances]: string} = {
    Alice: Alice.address,
    Bob: Bob.address,
    Ingrid: Ingrid.address,
  };
  await Promise.all([
    ...Object.keys(ethHoldings).map(async key => {
      expect(
        // eslint-disable-next-line @typescript-eslint/ban-ts-comment
        // @ts-ignore
        (await nitroAdjudicator.holdings(constants.AddressZero, internalDestinations[key])).eq(
          // eslint-disable-next-line @typescript-eslint/ban-ts-comment
          // @ts-ignore
          BigNumber.from(ethHoldings[key])
        )
      ).toBe(true);
    }),
    ...Object.keys(ethBalances).map(async key => {
      expect(
        // eslint-disable-next-line @typescript-eslint/ban-ts-comment
        // @ts-ignore
        (await provider.getBalance(externalDestinations[key])).eq(BigNumber.from(ethBalances[key]))
      ).toBe(true);
    }),
  ]);
}
