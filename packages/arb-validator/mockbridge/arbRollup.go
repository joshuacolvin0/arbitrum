/*
 * Copyright 2020, Offchain Labs, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *    http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package mockbridge

import (
	"context"
	"errors"
	"fmt"
	"github.com/offchainlabs/arbitrum/packages/arb-util/hashing"
	"github.com/offchainlabs/arbitrum/packages/arb-validator/arbbridge"
	"math/big"
	"sync"

	"github.com/offchainlabs/arbitrum/packages/arb-util/common"
	"github.com/offchainlabs/arbitrum/packages/arb-util/value"
	"github.com/offchainlabs/arbitrum/packages/arb-validator/structures"
)

const VALID_CHILD_TYPE = 3

type arbRollup struct {
	rollup *rollupData
	params structures.ChainParams
	Client *MockArbAuthClient
	mux    sync.Mutex
}

func newRollup(contractAddress common.Address, client *MockArbAuthClient) (*arbRollup, error) {
	//arbitrumRollupContract, err := rollup.NewArbRollup(address, client.(*ArbClient).client)
	//if err != nil {
	//	return nil, errors2.Wrap(err, "Failed to connect to arbRollup")
	//}
	//vm := &arbRollup{Client: client.(*MockArbClient).client, arbRollup: arbitrumRollupContract, auth: auth}

	// arbRollup init()
	// 	NodeGraph init()
	//	staking init()
	//        require(address(challengeFactory) == address(0), INIT_TWICE);
	//        require(_challengeFactoryAddress != address(0), INIT_NONZERO);
	//
	//        challengeFactory = IChallengeFactory(_challengeFactoryAddress);
	//
	//        // VM parameters
	//        stakeRequirement = _stakeRequirement;

	ru, ok := client.MockEthClient.rollups[contractAddress]
	if !ok {
		return nil, errors.New("Rollup contract not found")
	}

	roll := &arbRollup{
		rollup: ru,
		params: structures.ChainParams{
			StakeRequirement:        ru.escrowRequired,
			GracePeriod:             ru.gracePeriod,
			MaxExecutionSteps:       ru.maxSteps,
			ArbGasSpeedLimitPerTick: 200000,
		},
		Client: client,
	}
	return roll, nil
}

func (vm *arbRollup) PlaceStake(ctx context.Context, stakeAmount *big.Int, proof1 []common.Hash, proof2 []common.Hash) error {
	vm.mux.Lock()
	location := calculatePath(vm.rollup.lastConfirmed, proof1)
	leaf := calculatePath(location, proof2)
	if !vm.rollup.leaves[leaf] {
		return errors.New("invalid path proof")
	}
	if err := createStake(vm, stakeAmount, location); err != nil {
		return err
	}

	//emit RollupStakeCreated(msg.sender, location);
	event := arbbridge.StakeCreatedEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: vm.Client.MockEthClient.NextBlock,
		},
		Staker:   vm.Client.auth.From,
		NodeHash: location,
	}
	//vm.Client.MockEthClient.rollups[vm.rollup.contractAddress].events[vm.Client.MockEthClient.NextBlock] = append(vm.Client.MockEthClient.rollups[vm.rollup.contractAddress].events[vm.Client.MockEthClient.NextBlock], event)
	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
		Event: event,
		//Event: arbbridge.StakeCreatedEvent{
		//	ChainInfo: arbbridge.ChainInfo{
		//		BlockId: vm.Client.MockEthClient.NextBlock,
		//	},
		//	Staker:   vm.Client.auth.From,
		//	NodeHash: location,
		//},
	})
	vm.mux.Unlock()
	return nil
}

func createStake(vm *arbRollup, stakeAmount *big.Int, location common.Hash) error {
	// require(msg.value == stakeRequirement, STK_AMT);
	if stakeAmount != vm.rollup.escrowRequired {
		return errors.New("invalid stake amount")
	}
	if _, ok := vm.rollup.stakers[vm.Client.auth.From]; ok {
		return errors.New("staker already exists")
	}
	// require(stakers[msg.sender].location == 0x00, ALRDY_STAKED);
	vm.rollup.stakers[vm.Client.auth.From] = &staker{location, vm.Client.MockEthClient.NextBlock.Height, false, stakeAmount}
	//emit RollupStakeCreated(msg.sender, location);

	return nil
}

func refundStaker(vm *arbRollup, staker common.Address) {
	//refundStaker(stakerAddress);
	delete(vm.rollup.stakers, staker)
	// TODO:
	//transfer stake requirement
	// ???
	_ = append(vm.Client.MockEthClient.rollups[vm.Client.Address()].events[vm.Client.MockEthClient.NextBlock], arbbridge.StakeRefundedEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: vm.Client.MockEthClient.NextBlock,
		},
		Staker: staker,
	})
	//vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
	//	Event: arbbridge.StakeRefundedEvent{
	//		ChainInfo: arbbridge.ChainInfo{
	//			BlockId: vm.Client.MockEthClient.NextBlock,
	//		},
	//		Staker: staker,
	//	},
	//})

}

func (vm *arbRollup) RecoverStakeConfirmed(ctx context.Context, proof []common.Hash) error {
	vm.mux.Lock()
	//bytes32 stakerLocation = getStakerLocation(msg.sender);
	//require(RollupUtils.calculatePath(stakerLocation, proof) == latestConfirmed(), RECOV_PATH_PROOF);
	//refundStaker(stakerAddress);

	staker, ok := vm.rollup.stakers[vm.Client.auth.From]
	if !ok {
		return errors.New("staker not found")
	}

	if calculatePath(staker.location, proof) != vm.rollup.lastConfirmed {
		return errors.New("invalid path proof")
	}

	// refundStaker
	refundStaker(vm, vm.Client.auth.From)

	//emit RollupStakeRefunded(address(_stakerAddress));
	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
		Event: arbbridge.StakeRefundedEvent{
			ChainInfo: arbbridge.ChainInfo{
				BlockId: vm.Client.MockEthClient.NextBlock,
			},
			Staker: vm.Client.auth.From,
		},
	})
	vm.mux.Unlock()

	return nil
}

func (vm *arbRollup) RecoverStakeOld(ctx context.Context, staker common.Address, proof []common.Hash) error {
	vm.mux.Lock()
	//require(proof.length > 0, RECVOLD_LENGTH);
	if len(proof) <= 0 {
		return errors.New("proof must be non-zero length")
	}
	//_recoverStakeConfirmed(stakerAddress, proof);
	//bytes32 stakerLocation = getStakerLocation(msg.sender);
	st, ok := vm.rollup.stakers[staker]
	if !ok {
		return errors.New("staker not found")
	}
	//require(RollupUtils.calculatePath(stakerLocation, proof) == latestConfirmed(), RECOV_PATH_PROOF);
	if calculatePath(st.location, proof) != vm.rollup.lastConfirmed {
		return errors.New("invalid path proof")
	}
	refundStaker(vm, staker)
	vm.mux.Unlock()

	return nil
}

func (vm *arbRollup) RecoverStakeMooted(ctx context.Context, nodeHash common.Hash, staker common.Address, latestConfirmedProof []common.Hash, stakerProof []common.Hash) error {
	vm.mux.Lock()
	//bytes32 stakerLocation = getStakerLocation(msg.sender);
	//require(
	//	latestConfirmedProof[0] != stakerProof[0] &&
	//		RollupUtils.calculatePath(node, latestConfirmedProof) == latestConfirmed() &&
	//		RollupUtils.calculatePath(node, stakerProof) == stakerLocation,
	//	RECOV_CONFLICT_PROOF
	//);
	if latestConfirmedProof[0] == stakerProof[0] ||
		calculatePath(nodeHash, latestConfirmedProof) == vm.rollup.lastConfirmed ||
		calculatePath(nodeHash, stakerProof) != vm.rollup.stakers[vm.Client.auth.From].location {
		return errors.New("Invalid conflict proof")
	}
	//refundStaker(stakerAddress);
	refundStaker(vm, staker)
	vm.mux.Unlock()

	return nil
}

func (vm *arbRollup) RecoverStakePassedDeadline(
	ctx context.Context,
	stakerAddress common.Address,
	deadlineTicks *big.Int,
	disputableNodeHashVal common.Hash,
	childType uint64,
	vmProtoStateHash common.Hash,
	proof []common.Hash) error {
	vm.mux.Lock()
	//bytes32 stakerLocation = getStakerLocation(msg.sender);
	//bytes32 nextNode = RollupUtils.childNodeHash(
	//	stakerLocation,
	//	deadlineTicks,
	//	disputableNodeHashVal,
	//	childType,
	//	vmProtoStateHash
	//);
	//bytes32 leaf = RollupUtils.calculatePath(nextNode, proof);
	//???
	//require(isValidLeaf(leaf), RECOV_DEADLINE_LEAF);
	//require(block.number >= RollupTime.blocksToTicks(deadlineTicks), RECOV_DEADLINE_TIME);
	if common.TimeFromBlockNum(vm.Client.MockEthClient.NextBlock.Height).Val.Cmp(deadlineTicks) < 0 {
		return errors.New("Node is not passed deadline")
	}
	//refundStaker(stakerAddress);
	refundStaker(vm, stakerAddress)
	vm.mux.Unlock()

	return nil
}

func (vm *arbRollup) MoveStake(ctx context.Context, proof1 []common.Hash, proof2 []common.Hash) error {
	vm.mux.Lock()
	//bytes32 stakerLocation = getStakerLocation(msg.sender);
	//bytes32 newLocation = RollupUtils.calculatePath(stakerLocation, proof1);
	//bytes32 leaf = RollupUtils.calculatePath(newLocation, proof2);
	//require(isValidLeaf(leaf), MOVE_LEAF);
	//updateStakerLocation(msg.sender, newLocation);
	location := vm.rollup.stakers[vm.Client.auth.From].location
	newLocation := calculatePath(location, proof1)
	leaf := calculatePath(newLocation, proof2)
	if !vm.rollup.leaves[leaf] {
		return errors.New("MoveStake - invalid leaf")
	}
	vm.rollup.stakers[vm.Client.auth.From].location = newLocation
	//emit RollupStakeRefunded(address(_stakerAddress));
	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
		Event: arbbridge.StakeRefundedEvent{
			ChainInfo: arbbridge.ChainInfo{
				BlockId: vm.Client.MockEthClient.NextBlock,
			},
			Staker: vm.Client.auth.From,
		},
	})
	vm.mux.Unlock()

	return nil
}

func (vm *arbRollup) PruneLeaf(ctx context.Context, from common.Hash, leafProof []common.Hash, ancProof []common.Hash) error {
	vm.mux.Lock()
	//bytes32 leaf = RollupUtils.calculatePath(from, leafProof);
	fmt.Println("**********in PruneLeaf")
	leaf := calculatePath(from, leafProof)
	//require(isValidLeaf(leaf), PRUNE_LEAF);
	if !vm.rollup.leaves[leaf] {
		fmt.Println("MoveStake - invalid leaf")
		return errors.New("MoveStake - invalid leaf")
	}
	//require(
	//	leafProof[0] != latestConfirmedProof[0] &&
	//		RollupUtils.calculatePath(from, latestConfirmedProof) == latestConfirmed(),
	//	PRUNE_CONFLICT
	//);
	if leafProof[0] == ancProof[0] ||
		calculatePath(from, ancProof) != vm.rollup.lastConfirmed {
		return errors.New("prune conflict")
	}
	//delete leaves[leaf];
	delete(vm.rollup.leaves, leaf)
	//
	//emit RollupPruned(leaf);
	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
		Event: arbbridge.PrunedEvent{
			ChainInfo: arbbridge.ChainInfo{
				BlockId: vm.Client.MockEthClient.NextBlock,
			},
			Leaf: leaf,
		},
	})
	vm.mux.Unlock()

	return nil
}

func (vm *arbRollup) MakeAssertion(
	ctx context.Context,

	prevPrevLeafHash common.Hash,
	prevDataHash common.Hash,
	prevDeadline common.TimeTicks,
	prevChildType structures.ChildType,

	beforeState *structures.VMProtoData,
	assertionParams *structures.AssertionParams,
	assertionClaim *structures.AssertionClaim,
	stakerProof []common.Hash,
) error {
	vm.mux.Lock()
	//vm.auth.Context = ctx
	//tx, err := vm.arbRollup.MakeAssertion(
	//	vm.auth,
	//	[9][32]byte{
	//		beforeState.MachineHash,
	//		beforeState.PendingTop,
	//		prevPrevLeafHash,
	//		prevDataHash,
	//		assertionClaim.AfterPendingTop,
	//		assertionClaim.ImportedMessagesSlice,
	//		assertionClaim.AssertionStub.AfterHashValue(),
	//		assertionClaim.AssertionStub.LastMessageHashValue(),
	//		assertionClaim.AssertionStub.LastLogHashValue(),
	//	},
	//	beforeState.PendingCount,
	//	prevDeadline.Val,
	//	uint32(prevChildType),
	//	assertionParams.NumSteps,
	//	assertionParams.TimeBounds.AsIntArray(),
	//	assertionParams.ImportedMessageCount,
	//	assertionClaim.AssertionStub.DidInboxInsn,
	//	assertionClaim.AssertionStub.NumGas,
	//	stakerProof,
	//)
	//if err != nil {
	//	return err
	//}
	//return vm.waitForReceipt(ctx, tx, "MakeAssertion")
	//(bytes32 prevLeaf, bytes32 newValid) = makeAssertion(
	//	MakeAssertionData(
	//prevLeaf, newValid, err := vm.makeAssertion(assertionData)
	//if err != nil {
	//	return err
	//}
	//bytes32 vmProtoHashBefore = RollupUtils.protoStateHash(
	//data.beforeVMHash,
	//data.beforePendingTop,
	//data.beforePendingCount
	//);
	protoHashBefore := beforeState.Hash()
	//bytes32 prevLeaf = RollupUtils.childNodeHash(
	//data.prevPrevLeafHash,
	//data.prevDeadlineTicks,
	//data.prevDataHash,
	//data.prevChildType,
	//vmProtoHashBefore
	//);
	//rollupUtils in solidity
	prevLeaf, _ := structures.NodeHash(prevPrevLeafHash,
		protoHashBefore,
		prevDeadline,
		prevDataHash,
		prevChildType,
	)
	//require(isValidLeaf(prevLeaf), MAKE_LEAF);
	if !vm.rollup.leaves[prevLeaf] {
		return errors.New("makeAssertion - invalid leaf")
	}
	//require(!VM.isErrored(data.beforeVMHash) && !VM.isHalted(data.beforeVMHash), MAKE_RUN);
	// if
	//require(data.numSteps <= vmParams.maxExecutionSteps, MAKE_STEP);
	if assertionParams.NumSteps > vm.params.MaxExecutionSteps {
		return errors.New("makeAssertion - Tried to execute too many steps")
	}
	//require(VM.withinTimeBounds(data.timeBoundsBlocks), MAKE_TIME);
	//block.number >= _timeBoundsBlocks[0] && block.number <= _timeBoundsBlocks[1]
	//if !withinTimeBounds(assertionParams.TimeBounds) {
	if assertionParams.TimeBounds.IsValidTime(vm.Client.MockEthClient.NextBlock.Height) != nil {
		return errors.New("makeAssertion - Precondition: not within time bounds")
	}
	//require(data.importedMessageCount == 0 || data.didInboxInsn, MAKE_MESSAGES);
	if assertionParams.ImportedMessageCount.Cmp(big.NewInt(0)) != 0 && !assertionClaim.AssertionStub.DidInboxInsn {
		return errors.New("makeAssertion - Imported messages without reading them")
	}
	if (vm.Client.MockEthClient.pending[vm.rollup.contractAddress]) != nil {
		//(bytes32 pendingValue, uint256 pendingCount) = globalInbox.getPending();
		pendingTop := vm.Client.MockEthClient.pending[vm.rollup.contractAddress].pending
		//require(data.importedMessageCount <= pendingCount.sub(data.beforePendingCount), MAKE_MESSAGE_CNT);
		if assertionParams.ImportedMessageCount.Cmp(pendingTop.TopCount().Sub(pendingTop.TopCount(), beforeState.PendingCount)) > 0 {
			return errors.New("makeAssertion - Tried to import more messages than exist in pending inbox")
		}
	}
	//
	//uint256 gracePeriodTicks = vmParams.gracePeriodTicks;
	//uint256 checkTimeTicks = data.numArbGas / vmParams.arbGasSpeedLimitPerTick;
	//uint256 deadlineTicks = RollupTime.blocksToTicks(block.number) + gracePeriodTicks;
	//if (deadlineTicks < data.prevDeadlineTicks) {
	//deadlineTicks = data.prevDeadlineTicks;
	//}
	currentTicks := common.TimeFromBlockNum(vm.Client.MockEthClient.NextBlock.Height)
	deadlineTicks := currentTicks.Add(vm.params.GracePeriod)
	if deadlineTicks.Cmp(prevDeadline) < 0 {
		return errors.New("Node is not passed deadline")
	}
	//deadlineTicks += checkTimeTicks;
	checkTimeTicks := vm.params.StakeRequirement.Div(vm.params.StakeRequirement, big.NewInt(int64(vm.params.ArbGasSpeedLimitPerTick)))
	deadlineTicks = deadlineTicks.Add(common.TimeFromSeconds(checkTimeTicks.Int64()))
	protoStateHash := hashing.SoliditySHA3(
		hashing.Bytes32(assertionClaim.AssertionStub.AfterHash),
		hashing.Bytes32(assertionClaim.AfterPendingTop),
		hashing.Uint256(beforeState.PendingCount.Add(beforeState.PendingCount, assertionParams.ImportedMessageCount)),
	)
	protoData := hashing.SoliditySHA3(
		hashing.Bytes32(assertionClaim.AssertionStub.LastMessageHash),
		hashing.Bytes32(assertionClaim.AssertionStub.LastLogHash),
	)

	//
	//invalidPending := generateInvalidPendingTopLeaf(vm, beforeState, prevLeaf, deadlineTicks, assertionClaim, assertionParams)
	// prev - prevLeaf
	// protoHash - calculate
	// deadlineTicks - ticks
	// protoData - calculate

	// protoData --
	// valid - hash of -
	// lastMsgHash
	// lastLogHash
	// invalid pending - hash of
	// challengeDataHash - hash of
	// after pending top
	// max pending top
	// pending left -
	// maxPendingCount - (pending count + importedMsgCount)
	// challenge Period
	// gracePeriod + 1
	// invalid Msgs
	// challengeDataHash - hash of
	// pendingTop
	// afterPendingTop
	// empty tuple hash
	// importedMsgsSlice
	// importedMsgCount
	// challenge Period
	// gracePeriod + 1
	// invalid execution
	// challengeDataHash - hash of
	// num steps
	// ExecutionPreconditionHash -
	// pre.BeforeHash - vmProtoData.MachineHash
	// pre.TimeBounds.Start - node.disputable.AssertionParams.TimeBounds.start
	// pre.TimeBounds.End - node.disputable.AssertionParams.TimeBounds.end
	// pre.BeforeInbox.Hash() - value.NewHashOnlyValue(node.disputable.AssertionClaim.ImportedMessagesSlice, 0)
	// assertion stub hash
	// challenge Period
	// GracePeriod.Add(node.disputable.CheckTime(params))
	// protoHash  -
	// machineHash
	// pendingTop
	// pendingCount

	var pendingTopCount *big.Int
	var pendingTopHash common.Hash
	globalInboxPending, ok := vm.Client.MockEthClient.pending[vm.rollup.contractAddress]
	if !ok {
		pendingTopCount = big.NewInt(0)
		pendingTopHash = value.NewEmptyTuple().Hash()
	} else {
		pendingTopCount = globalInboxPending.pending.TopCount()
		pendingTopHash = globalInboxPending.pending.GetTopHash()
	}
	left := new(big.Int).Add(beforeState.PendingCount, assertionParams.ImportedMessageCount)
	left = left.Sub(pendingTopCount, left)
	invPendingChallengeDataHash := structures.PendingTopChallengeDataHash(
		assertionClaim.AfterPendingTop,
		pendingTopHash,
		left,
	)
	ticks := vm.params.GracePeriod.Add(common.TimeFromBlockNum(common.NewTimeBlocks(big.NewInt(1))))
	invPendingProtoData := hashing.SoliditySHA3(
		hashing.Bytes32(invPendingChallengeDataHash),
		hashing.TimeTicks(ticks),
	)
	invalidPending, _ := structures.NodeHash(prevLeaf,
		protoHashBefore,
		deadlineTicks,
		invPendingProtoData,
		structures.InvalidPendingChildType)

	invMsgsChallengeDataHash := structures.MessageChallengeDataHash(
		beforeState.PendingTop,
		assertionClaim.AfterPendingTop,
		value.NewEmptyTuple().Hash(),
		assertionClaim.ImportedMessagesSlice,
		assertionParams.ImportedMessageCount,
	)
	invMsgsProtoData := hashing.SoliditySHA3(
		hashing.Bytes32(invMsgsChallengeDataHash),
		hashing.TimeTicks(vm.params.GracePeriod.Add(common.TimeFromBlockNum(common.NewTimeBlocks(big.NewInt(1))))),
	)
	invalidMessages, _ := structures.NodeHash(prevLeaf,
		protoHashBefore,
		deadlineTicks,
		invMsgsProtoData,
		structures.InvalidMessagesChildType)

	invExecChallengeDataHash := structures.ExecutionDataHash(
		assertionParams.NumSteps,
		structures.ExecutionPreconditionHash(beforeState.MachineHash, assertionParams.TimeBounds, assertionClaim.ImportedMessagesSlice),
		assertionClaim.AssertionStub.Hash(),
	)
	invExecProtoData := hashing.SoliditySHA3(
		hashing.Bytes32(invExecChallengeDataHash),
		hashing.TimeTicks(vm.params.GracePeriod.Add(common.TimeTicks{new(big.Int).SetUint64(assertionClaim.AssertionStub.NumGas / vm.params.ArbGasSpeedLimitPerTick)})),
	)
	invalidExecution, _ := structures.NodeHash(prevLeaf,
		protoHashBefore,
		deadlineTicks,
		invExecProtoData,
		structures.InvalidExecutionChildType,
	)

	valid, _ := structures.NodeHash(prevLeaf,
		protoStateHash,
		deadlineTicks,
		protoData,
		structures.ValidChildType,
	)
	//
	//leaves[invalidPending] = true;
	vm.rollup.leaves[invalidPending] = true
	//leaves[invalidMessages] = true;
	vm.rollup.leaves[invalidMessages] = true
	//leaves[invalidExec] = true;
	vm.rollup.leaves[invalidExecution] = true
	//leaves[validHash] = true;
	vm.rollup.leaves[valid] = true
	//delete leaves[prevLeaf];
	delete(vm.rollup.leaves, prevLeaf)
	//
	//emitAssertedEvent(data, prevLeaf, pendingValue, pendingCount);
	//ChainInfo
	//PrevLeafHash    common.Hash
	//Params          *structures.AssertionParams
	//Claim           *structures.AssertionClaim
	//MaxPendingTop   common.Hash
	//MaxPendingCount *big.Int
	//	emit RollupAsserted(
	//		[
	//			prevLeaf,
	//		pendingValue,
	//		data.afterPendingTop,
	//		data.importedMessagesSlice,
	//		data.afterVMHash,
	//		data.messagesAccHash,
	//		data.logsAccHash
	//],
	//	pendingCount,
	//	data.importedMessageCount,
	//	data.timeBoundsBlocks,
	//	data.numArbGas,
	//	data.numSteps,
	//	data.didInboxInsn
	//	);

	event := arbbridge.AssertedEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: vm.Client.MockEthClient.NextBlock,
		},
		PrevLeafHash:    prevLeaf,
		Params:          assertionParams,
		Claim:           assertionClaim,
		MaxPendingTop:   beforeState.PendingTop,
		MaxPendingCount: beforeState.PendingCount,
	}
	//vm.Client.MockEthClient.rollups[vm.rollup.contractAddress].events[vm.Client.MockEthClient.NextBlock] = append(vm.Client.MockEthClient.rollups[vm.rollup.contractAddress].events[vm.Client.MockEthClient.NextBlock], event)
	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
		Event: event,
	})
	//return (prevLeaf, validHash);

	//bytes32 stakerLocation = getStakerLocation(msg.sender);
	//require(RollupUtils.calculatePath(stakerLocation, _stakerProof) == prevLeaf, MAKE_STAKER_PROOF);
	if calculatePath(vm.rollup.stakers[vm.Client.auth.From].location, stakerProof) != prevLeaf {
		return errors.New("invalid staker location proof")
	}

	//updateStakerLocation(msg.sender, newValid);
	vm.rollup.stakers[vm.Client.auth.From].location = valid
	vm.rollup.nextConfirmed = valid
	//emit RollupStakeRefunded(address(_stakerAddress));
	stakeMovedEvent := arbbridge.StakeMovedEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: vm.Client.MockEthClient.NextBlock,
		},
		Staker:   vm.Client.auth.From,
		Location: valid,
	}
	fmt.Println("Publishing StakeMovedEvent to location", valid)
	//vm.Client.MockEthClient.rollups[vm.rollup.contractAddress].events[vm.Client.MockEthClient.NextBlock] = append(vm.Client.MockEthClient.rollups[vm.rollup.contractAddress].events[vm.Client.MockEthClient.NextBlock], stakeRefundedEvent)
	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
		Event: stakeMovedEvent,
	})
	vm.mux.Unlock()

	return nil
}

func (vm *arbRollup) ConfirmValid(
	ctx context.Context,
	deadline common.TimeTicks,
	outMsgs []value.Value,
	logsAccHash common.Hash,
	protoHash common.Hash,
	stakerAddresses []common.Address,
	stakerProofs []common.Hash,
	stakerProofOffsets []*big.Int,
) error {
	//vm.mux.Lock()
	fmt.Println("   ----  in ConfirmValid")
	//vm.auth.Context = ctx
	//messages := hashing.CombineMessages(outMsgs)
	//tx, err := vm.arbRollup.ConfirmValid(
	//	vm.auth,
	//	deadline.Val,
	//	messages,
	//	logsAccHash,
	//	protoHash,
	//	stakerAddresses,
	//	stakerProofs,
	//	stakerProofOffsets,
	//)
	//if err != nil {
	//	return err
	//}
	//return vm.waitForReceipt(ctx, tx, "ConfirmValid")

	//confirmNode
	//_confirmNode(
	//	deadlineTicks,
	//	RollupUtils.validDataHash(
	//		Protocol.generateLastMessageHash(_messages),
	//		logsAcc
	//),
	//VALID_CHILD_TYPE,
	//	vmProtoStateHash,
	//	stakerAddresses,
	//	stakerProofs,
	//	stakerProofOffsets
	//);
	//bytes32 to = RollupUtils.childNodeHash(
	//	latestConfirmed(),
	//	deadlineTicks,
	//	nodeDataHash,
	//	branch,
	//	vmProtoStateHash
	//);
	currentTicks := common.TimeFromBlockNum(vm.Client.MockEthClient.NextBlock.Height)
	deadlineTicks := currentTicks.Add(vm.params.GracePeriod)

	valid, _ := structures.NodeHash(vm.rollup.lastConfirmed,
		protoHash,
		deadlineTicks,
		vm.rollup.lastConfirmed, //??????????
		structures.ValidChildType,
	)
	valid = vm.rollup.nextConfirmed

	//?????????????
	//require(RollupTime.blocksToTicks(block.number) >= deadlineTicks, CONF_TIME);
	//uint activeCount = checkAlignedStakers(
	//	to,
	//	deadlineTicks,
	//	stakerAddresses,
	//	stakerProofs,
	//	stakerProofOffsets
	//);
	//uint256 _stakerCount = stakerAddresses.length;
	//require(_stakerCount == stakerCount, CHCK_COUNT);
	//require(_stakerCount + 1 == stakerProofOffsets.length, CHCK_OFFSETS);
	//bytes20 prevStaker = 0x00;
	//uint activeCount = 0;
	//for (uint256 i = 0; i < _stakerCount; i++) {
	//	address stakerAddress = stakerAddresses[i];
	//	require(bytes20(stakerAddress) > prevStaker, CHCK_ORDER);
	//	Staker storage staker = getValidStaker(stakerAddress);
	//	if (RollupTime.blocksToTicks(staker.creationTimeBlocks) < deadlineTicks) {
	//		require(
	//			RollupUtils.calculatePathOffset(
	//				node,
	//				stakerProofs,
	//				stakerProofOffsets[i],
	//				stakerProofOffsets[i+1]
	//		) == staker.location,
	//			CHCK_STAKER_PROOF
	//		);
	//		activeCount++;
	//	}
	//	prevStaker = bytes20(stakerAddress);
	//}
	//return activeCount;

	//require(activeCount > 0, CONF_HAS_STAKER);
	//
	//confirmNode(to);
	//latestConfirmedPriv = to;
	//emit RollupConfirmed(to);
	vm.rollup.lastConfirmed = valid

	ConfirmedEvent := arbbridge.ConfirmedEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: vm.Client.MockEthClient.NextBlock,
		},
		NodeHash: valid,
	}
	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
		Event: ConfirmedEvent,
	})

	//
	//globalInbox.sendMessages(_messages);
	// ConfirmedAssertionEvent
	//emit ConfirmedAssertion(
	//	logsAcc
	//);
	ConfirmedAssertionEvent := arbbridge.ConfirmedAssertionEvent{
		ChainInfo: arbbridge.ChainInfo{
			BlockId: vm.Client.MockEthClient.NextBlock,
		},
		LogsAccHash: logsAccHash,
	}
	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
		Event: ConfirmedAssertionEvent,
	})

	return nil
}

//func (vm *arbRollup) confirmNode(
//	ctx context.Context,
//	deadline common.TimeTicks,
//	challengeNodeData []common.Address,
//	branch structures.ChildType,
//	protoHash common.Hash,
//	stakerAddresses []common.Address,
//	stakerProofs []common.Hash,
//	stakerProofOffsets []*big.Int,
//	//uint256 deadlineTicks,
//	//bytes32 challengeNodeData,
//	//uint256 branch,
//	//bytes32 vmProtoStateHash,
//	//address[] calldata stakerAddresses,
//	//bytes32[] calldata stakerProofs,
//	//uint256[] calldata stakerProofOffsets
//) error {
//	//_confirmNode(
//	//	deadlineTicks,
//	//	RollupUtils.validDataHash(
//	//		Protocol.generateLastMessageHash(_messages),
//	//vm.Client.MockEthClient.pending[vm.address].pending.GetTopHash()
//	//		logsAcc
//	//),
//	//VALID_CHILD_TYPE,
//	//	vmProtoStateHash,
//	//	stakerAddresses,
//	//	stakerProofs,
//	//	stakerProofOffsets
//	//);
//	//bytes32 to = RollupUtils.childNodeHash(
//	//	latestConfirmed(),
//	//	deadlineTicks,
//	//	nodeDataHash,
//	//	branch,
//	//	vmProtoStateHash
//	//);
//	//require(RollupTime.blocksToTicks(block.number) >= deadlineTicks, CONF_TIME);
//	//uint activeCount = checkAlignedStakers(
//	//	to,
//	//	deadlineTicks,
//	//	stakerAddresses,
//	//	stakerProofs,
//	//	stakerProofOffsets
//	//);
//	//uint256 _stakerCount = stakerAddresses.length;
//	//require(_stakerCount == stakerCount, CHCK_COUNT);
//	//require(_stakerCount + 1 == stakerProofOffsets.length, CHCK_OFFSETS);
//	//bytes20 prevStaker = 0x00;
//	//uint activeCount = 0;
//	//for (uint256 i = 0; i < _stakerCount; i++) {
//	//	address stakerAddress = stakerAddresses[i];
//	//	require(bytes20(stakerAddress) > prevStaker, CHCK_ORDER);
//	//	Staker storage staker = getValidStaker(stakerAddress);
//	//	if (RollupTime.blocksToTicks(staker.creationTimeBlocks) < deadlineTicks) {
//	//		require(
//	//			RollupUtils.calculatePathOffset(
//	//				node,
//	//				stakerProofs,
//	//				stakerProofOffsets[i],
//	//				stakerProofOffsets[i+1]
//	//		) == staker.location,
//	//			CHCK_STAKER_PROOF
//	//		);
//	//		activeCount++;
//	//	}
//	//	prevStaker = bytes20(stakerAddress);
//	//}
//	//return activeCount;
//
//	//require(activeCount > 0, CONF_HAS_STAKER);
//	//
//	//confirmNode(to);
//	//latestConfirmedPriv = to;
//	//emit RollupConfirmed(to);
//	ConfirmedAssertionEvent := arbbridge.{
//		ChainInfo: arbbridge.ChainInfo{
//			BlockId: vm.Client.MockEthClient.NextBlock,
//		},
//		LogsAccHash: logsAccHash,
//	}
//	vm.Client.MockEthClient.pubMsg(arbbridge.MaybeEvent{
//		Event: ConfirmedAssertionEvent,
//	})
//	fmt.Println("  ---  in ConfirmNode")
//	return nil
//}

func (vm *arbRollup) ConfirmInvalid(
	ctx context.Context,
	deadline common.TimeTicks,
	challengeNodeData common.Hash,
	branch structures.ChildType,
	protoHash common.Hash,
	stakerAddresses []common.Address,
	stakerProofs []common.Hash,
	stakerProofOffsets []*big.Int,
) error {
	//vm.mux.Lock()
	fmt.Println("   ----  in ConfirmInvalid")
	//vm.auth.Context = ctx
	//tx, err := vm.arbRollup.ConfirmInvalid(
	//	vm.auth,
	//	deadline.Val,
	//	challengeNodeData,
	//	new(big.Int).SetUint64(uint64(branch)),
	//	protoHash,
	//	stakerAddresses,
	//	stakerProofs,
	//	stakerProofOffsets,
	//)
	//if err != nil {
	//	return err
	//}
	//return vm.waitForReceipt(ctx, tx, "ConfirmInvalid")
	//require(branch < VALID_CHILD_TYPE, CONF_INV_TYPE);
	if branch >= VALID_CHILD_TYPE {
		return errors.New("Type is not invalid")
	}

	//return vm.confirmNode(ctx, deadline, challengeNodeData, branch, protoHash, stakerAddresses, stakerProofs, stakerProofOffsets)
	return nil
}

func (vm *arbRollup) StartChallenge(
	ctx context.Context,
	asserterAddress common.Address,
	challengerAddress common.Address,
	prevNode common.Hash,
	disputableDeadline *big.Int,
	asserterPosition structures.ChildType,
	challengerPosition structures.ChildType,
	asserterVMProtoHash common.Hash,
	challengerVMProtoHash common.Hash,
	asserterProof []common.Hash,
	challengerProof []common.Hash,
	asserterNodeHash common.Hash,
	challengerDataHash common.Hash,
	challengerPeriodTicks common.TimeTicks,
) error {
	//vm.mux.Lock()
	//vm.auth.Context = ctx
	//tx, err := vm.arbRollup.StartChallenge(
	//	vm.auth,
	//	asserterAddress,
	//	challengerAddress,
	//	prevNode,
	//	disputableDeadline,
	//	[2]*big.Int{
	//		new(big.Int).SetUint64(uint64(asserterPosition)),
	//		new(big.Int).SetUint64(uint64(challengerPosition)),
	//	},
	//	[2][32]byte{
	//		asserterVMProtoHash,
	//		challengerVMProtoHash,
	//	},
	//	asserterProof,
	//	challengerProof,
	//	asserterDataHash,
	//	asserterPeriodTicks.Val,
	//	challengerNodeHash,
	//)
	//if err != nil {
	//	return err
	//}
	//return vm.waitForReceipt(ctx, tx, "StartExecutionChallenge")
	return nil
}

func (vm *arbRollup) IsStaked(address common.Address) (bool, error) {
	return false, nil
}

//func (vm *arbRollup) VerifyVM(
//	auth *bind.CallOpts,
//	config *valmessage.VMConfiguration,
//	machine [32]byte,
//) error {
//	//code, err := vm.contract.Client.CodeAt(auth.Context, vm.address, nil)
//	// Verify that VM has correct code
//	vmInfo, err := vm.arbRollup.Vm(auth)
//	if err != nil {
//		return err
//	}
//
//	if vmInfo.MachineHash != machine {
//		return errors.New("VM has different machine hash")
//	}
//
//	if config.GracePeriod != uint64(vmInfo.GracePeriod) {
//		return errors.New("VM has different grace period")
//	}
//
//	if value.NewBigIntFromBuf(config.EscrowRequired).Cmp(vmInfo.EscrowRequired) != 0 {
//		return errors.New("VM has different escrow required")
//	}
//
//	if config.MaxExecutionStepCount != vmInfo.MaxExecutionSteps {
//		return errors.New("VM has different mxa steps")
//	}
//
//	owner, err := vm.arbRollup.Owner(auth)
//	if err != nil {
//		return err
//	}
//	if protocol.NewAddressFromBuf(config.Owner) != owner {
//		return errors.New("VM has different owner")
//	}
//	return nil
//}

//func (vm *arbRollup) waitForReceipt(ctx context.Context, tx *types.Transaction, methodName string) error {
//	return waitForReceipt(ctx, vm.Client, vm.auth.From, tx, methodName)
//}
