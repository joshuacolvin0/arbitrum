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

package checkpointing

import (
	"context"
	"errors"
	"log"
	"math/big"
	"os"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/offchainlabs/arbitrum/packages/arb-avm-cpp/cmachine"
	"github.com/offchainlabs/arbitrum/packages/arb-checkpointer/ckptcontext"
	"github.com/offchainlabs/arbitrum/packages/arb-util/common"
	"github.com/offchainlabs/arbitrum/packages/arb-util/machine"
	"github.com/offchainlabs/arbitrum/packages/arb-util/value"
	"github.com/offchainlabs/arbitrum/packages/arb-validator-core/arbbridge"
)

var errNoCheckpoint = errors.New("cannot restore because no checkpoint exists")
var errNoMatchingCheckpoint = errors.New("cannot restore because no matching checkpoint exists")

type IndexedCheckpointer struct {
	*sync.Mutex
	db                    *cmachine.CheckpointStorage
	bs                    machine.BlockStore
	nextCheckpointToWrite *writableCheckpoint
	maxReorgHeight        *big.Int
}

func NewIndexedCheckpointer(
	rollupAddr common.Address,
	databasePath string,
	maxReorgHeight *big.Int,
	forceFreshStart bool,
) (*IndexedCheckpointer, error) {
	ret, err := newIndexedCheckpointer(
		rollupAddr,
		databasePath,
		new(big.Int).Set(maxReorgHeight),
		forceFreshStart,
	)

	if err != nil {
		return nil, err
	}

	go ret.writeDaemon()
	go cleanupDaemon(ret.bs, ret.db, maxReorgHeight)
	return ret, nil
}

// newIndexedCheckpointerFactory creates the checkpointer, but doesn't
// launch it's reading and writing threads. This is useful for deterministic
// testing
func newIndexedCheckpointer(
	rollupAddr common.Address,
	databasePath string,
	maxReorgHeight *big.Int,
	forceFreshStart bool,
) (*IndexedCheckpointer, error) {
	if databasePath == "" {
		databasePath = MakeCheckpointDatabasePath(rollupAddr)
	}
	if forceFreshStart {
		// for testing only --  delete old database to get fresh start
		if err := os.RemoveAll(databasePath); err != nil {
			return nil, err
		}
	}
	cCheckpointer, err := cmachine.NewCheckpoint(databasePath)
	if err != nil {
		return nil, err
	}

	return &IndexedCheckpointer{
		new(sync.Mutex),
		cCheckpointer,
		cCheckpointer.GetBlockStore(),
		nil,
		maxReorgHeight,
	}, nil
}

func (cp *IndexedCheckpointer) Initialize(arbitrumCodeFilePath string) error {
	return cp.db.Initialize(arbitrumCodeFilePath)
}

func (cp *IndexedCheckpointer) Initialized() bool {
	return cp.db.Initialized()
}

func (cp *IndexedCheckpointer) MaxReorgHeight() *big.Int {
	return new(big.Int).Set(cp.maxReorgHeight)
}

func (cp *IndexedCheckpointer) GetAggregatorStore() *cmachine.AggregatorStore {
	return cp.db.GetAggregatorStore()
}

// HasCheckpointedState checks whether the block store is empty, which is the table
// which contains the checkpoints recorded by the IndexedCheckpointer
func (cp *IndexedCheckpointer) HasCheckpointedState() bool {
	return !cp.bs.IsBlockStoreEmpty()
}

func (cp *IndexedCheckpointer) GetInitialMachine() (machine.Machine, error) {
	return cp.db.GetInitialMachine()
}

type writableCheckpoint struct {
	blockId  *common.BlockId
	contents []byte
	ckpCtx   *ckptcontext.CheckpointContext
	errChan  chan<- error
}

func (cp *IndexedCheckpointer) AsyncSaveCheckpoint(
	blockId *common.BlockId,
	contents []byte,
	cpCtx *ckptcontext.CheckpointContext,
) <-chan error {
	cp.Lock()
	defer cp.Unlock()

	errChan := make(chan error, 1)

	if cp.nextCheckpointToWrite != nil {
		cp.nextCheckpointToWrite.errChan <- errors.New("replaced by newer checkpoint")
		close(cp.nextCheckpointToWrite.errChan)
	}

	cp.nextCheckpointToWrite = &writableCheckpoint{
		blockId:  blockId,
		contents: contents,
		ckpCtx:   cpCtx,
		errChan:  errChan,
	}

	return errChan
}

func (cp *IndexedCheckpointer) RestoreLatestState(ctx context.Context, clnt arbbridge.ChainTimeGetter, unmarshalFunc func([]byte, ckptcontext.RestoreContext, *common.BlockId) error) error {
	return restoreLatestState(ctx, cp.bs, cp.db, clnt, unmarshalFunc)
}

func restoreLatestState(
	ctx context.Context,
	bs machine.BlockStore,
	db machine.CheckpointStorage,
	clnt arbbridge.ChainTimeGetter,
	unmarshalFunc func([]byte, ckptcontext.RestoreContext, *common.BlockId) error,
) error {
	if bs.IsBlockStoreEmpty() {
		return errNoCheckpoint
	}

	startHeight := bs.MaxBlockStoreHeight()
	lowestHeight := bs.MinBlockStoreHeight()

	for height := startHeight; height.Cmp(lowestHeight) >= 0; height = common.NewTimeBlocks(new(big.Int).Sub(height.AsInt(), big.NewInt(1))) {
		onchainId, err := clnt.BlockIdForHeight(ctx, height)
		if err != nil {
			return err
		}
		blockData, err := bs.GetBlock(onchainId)
		if err != nil {
			// If no record was found, try the next block
			continue
		}
		ckpWithMan := &CheckpointWithManifest{}
		if err := proto.Unmarshal(blockData, ckpWithMan); err != nil {
			// If something went wrong, try the next block
			continue
		}
		if err := unmarshalFunc(ckpWithMan.Contents, &restoreContextLocked{db}, onchainId); err != nil {
			log.Println("Failed load checkpoint at height", height, "with error", err)
			continue
		}
		return nil
	}
	return errNoMatchingCheckpoint
}

func (cp *IndexedCheckpointer) writeDaemon() {
	ticker := time.NewTicker(common.NewTimeBlocksInt(2).Duration())
	defer ticker.Stop()
	for {
		<-ticker.C
		cp.Lock()
		checkpoint := cp.nextCheckpointToWrite
		cp.nextCheckpointToWrite = nil
		cp.Unlock()
		if checkpoint != nil {
			err := writeCheckpoint(cp.bs, cp.db, checkpoint)
			if err != nil {
				log.Println("Error writing checkpoint: {}", err)
			}
			checkpoint.errChan <- err
			close(checkpoint.errChan)
		}
	}
}

func writeCheckpoint(bs machine.BlockStore, db machine.CheckpointStorage, wc *writableCheckpoint) error {
	// save values and machines
	if err := ckptcontext.SaveCheckpointContext(db, wc.ckpCtx); err != nil {
		return err
	}

	// save main checkpoint data
	ckpWithMan := &CheckpointWithManifest{
		Contents: wc.contents,
		Manifest: wc.ckpCtx.Manifest(),
	}
	bytesBuf, err := proto.Marshal(ckpWithMan)
	if err != nil {
		return err
	}
	if err := bs.PutBlock(wc.blockId, bytesBuf); err != nil {
		return errors.New("failed to write checkpoint to checkpoint db")
	}

	return nil
}

func cleanupDaemon(bs machine.BlockStore, db machine.CheckpointStorage, maxReorgHeight *big.Int) {
	ticker := time.NewTicker(common.NewTimeBlocksInt(25).Duration())
	defer ticker.Stop()
	for {
		<-ticker.C
		cleanup(bs, db, maxReorgHeight)
	}
}

func cleanup(bs machine.BlockStore, db machine.CheckpointStorage, maxReorgHeight *big.Int) {
	currentMin := bs.MinBlockStoreHeight()
	currentMax := bs.MaxBlockStoreHeight()
	height := common.NewTimeBlocks(new(big.Int).Sub(currentMin.AsInt(), big.NewInt(1)))
	heightLimit := common.NewTimeBlocks(new(big.Int).Sub(currentMax.AsInt(), maxReorgHeight))
	var prevIds []*common.BlockId
	for height.Cmp(heightLimit) < 0 {
		blockIds := bs.BlocksAtHeight(height)
		if len(blockIds) > 0 {
			for _, id := range prevIds {
				_ = deleteCheckpointForKey(bs, db, id)
			}
			prevIds = blockIds
		}
		height = common.NewTimeBlocks(new(big.Int).Add(height.AsInt(), big.NewInt(1)))
	}
}

func deleteCheckpointForKey(bs machine.BlockStore, db machine.CheckpointStorage, id *common.BlockId) error {
	val, err := bs.GetBlock(id)
	if err != nil {
		return err
	}
	ckp := &CheckpointWithManifest{}
	if err := proto.Unmarshal(val, ckp); err != nil {
		return err
	}
	_ = bs.DeleteBlock(id) // ignore error
	if ckp.Manifest != nil {
		for _, hbuf := range ckp.Manifest.Values {
			h := hbuf.Unmarshal()
			_ = db.DeleteValue(h) // ignore error
		}
		for _, hbuf := range ckp.Manifest.Machines {
			h := hbuf.Unmarshal()
			_ = db.DeleteCheckpoint(h) // ignore error
		}
	}
	return nil
}

type restoreContextLocked struct {
	db machine.CheckpointStorage
}

func (rcl *restoreContextLocked) GetValue(h common.Hash) value.Value {
	return rcl.db.GetValue(h)
}

func (rcl *restoreContextLocked) GetMachine(h common.Hash) machine.Machine {
	ret, err := rcl.db.GetMachine(h)
	if err != nil {
		log.Fatal(err)
	}
	return ret
}
