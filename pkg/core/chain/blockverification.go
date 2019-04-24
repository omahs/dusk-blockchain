package chain

import (
	"bytes"
	"errors"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/block"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/database"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/transactions"
	"time"
)

// AcceptBlock will accept a block if
// 1. We have not seen it before
// 2. All stateless and statefull checks are true
// Returns nil, if checks passed and block was successfully saved
func (c *Chain) AcceptBlock(blk block.Block) error {

	// 1. Check that we have not seen this block before
	err := c.db.View(func(t database.Transaction) error {
		_, err := t.FetchBlockExists(blk.Header.Hash)
		return err
	})

	if err != database.ErrBlockNotFound {
		if err == nil {
			err = errors.New("block already exists")
		}
		return err
	}

	// 2. Check that stateless and stateful checks pass
	if err := c.verifyBlock(blk); err != nil {
		return err
	}

	// 3. Save block in database
	err = c.db.Update(func(t database.Transaction) error {
		return t.StoreBlock(&blk)
	})

	if err != nil {
		return err
	}

	c.PrevBlock = blk

	// 4. Clean up all txs from the mempool that have been already
	// added to the chain.
	c.m.RemoveAccepted(blk)

	// 5. Gossip block
	if err := c.propagateBlock(blk); err != nil {
		return err
	}

	return nil
}

// VerifyBlock will verify whether a block is valid according to the rules of the consensus
// returns nil if a block is valid
func (c Chain) verifyBlock(blk block.Block) error {
	if err := c.checkBlockHeader(blk); err != nil {
		return nil
	}

	if err := checkMultiCoinbases(blk.Txs); err != nil {
		return err
	}

	for i, merklePayload := range blk.Txs {
		tx, ok := merklePayload.(transactions.Transaction)
		if !ok {
			return errors.New("tx does not implement the transaction interface")
		}
		if err := c.verifyTX(uint64(i), uint64(blk.Header.Timestamp), tx); err != nil {
			return err
		}
	}
	return nil
}

// checks whether a block header is malformed,
// These are stateless and stateful checks
// returns nil, if all checks pass
func (c Chain) checkBlockHeader(blk block.Block) error {
	// Version
	if blk.Header.Version > 0 {
		return errors.New("unsupported block version")
	}

	// blk.Headerhash = prevHeaderHash
	if !bytes.Equal(blk.Header.PrevBlock, c.PrevBlock.Header.Hash) {
		return errors.New("Previous block hash does not equal the previous hash in the current block")
	}

	// blk.Headerheight = prevHeaderHeight +1
	if blk.Header.Height != c.PrevBlock.Header.Height+1 {
		return errors.New("current block height is not one plus the previous block height")
	}

	// blk.Timestamp > prevTimestamp
	if blk.Header.Timestamp <= c.PrevBlock.Header.Timestamp {
		return errors.New("current timestamp is less than the previous timestamp")
	}

	// Merkle tree check -- Check is here as the root is not calculated on decode
	tR := blk.Header.TxRoot
	if err := blk.SetRoot(); err != nil {
		return errors.New("could not calculate the merkle tree root for this header")
	}

	if !bytes.Equal(tR, blk.Header.TxRoot) {
		return errors.New("merkle root mismatch")
	}

	return nil
}

// ForgeCandidateBlock has the knowledge to forge the next block to propose
func (c Chain) ForgeCandidateBlock() (*block.Block, error) {

	// TODO Missing fields for forging the block
	// - Seed
	// - CertHash

	txs := c.m.GetVerifiedTxs()

	// TODO guard the prevBlock with mutex
	nextHeight := c.PrevBlock.Header.Height + 1
	prevHash := c.PrevBlock.Header.Hash

	h := &block.Header{
		Version:   0,
		Timestamp: time.Now().Unix(),
		Height:    nextHeight,
		PrevBlock: prevHash,
		TxRoot:    nil,

		Seed:     nil,
		CertHash: nil,
	}

	// Generate the candidate block
	b := &block.Block{
		Header: h,
		Txs:    txs,
	}

	// Update TxRoot
	err := b.SetRoot()

	if err != nil {
		return nil, err
	}

	// Generate the block hash
	err = b.SetHash()

	if err != nil {
		return nil, err
	}

	// Ensure the forged block satisfies all chain rules
	if err := c.verifyBlock(*b); err != nil {
		return nil, err
	}

	return b, nil
}
