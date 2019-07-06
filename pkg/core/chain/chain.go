package chain

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sync"

	"math/big"

	//"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus"

	logger "github.com/sirupsen/logrus"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/peer/peermsg"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/protocol"

	"github.com/bwesterb/go-ristretto"
	cfg "gitlab.dusk.network/dusk-core/dusk-go/pkg/config"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/block"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/committee"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/msg"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/user"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/database"
	_ "gitlab.dusk.network/dusk-core/dusk-go/pkg/core/database/heavy"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/transactions"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/verifiers"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/encoding"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/topics"
	"gitlab.dusk.network/dusk-core/zkproof"
)

var log *logger.Entry = logger.WithFields(logger.Fields{"process": "chain"})

// Chain represents the nodes blockchain
// This struct will be aware of the current state of the node.
type Chain struct {
	eventBus  *wire.EventBus
	rpcBus    *wire.RPCBus
	db        database.DB
	committee committee.Foldable

	prevBlock block.Block
	// protect prevBlock with mutex as it's touched out of the main chain loop
	// by SubscribeCallback.
	// TODO: Consider if mutex can be removed
	mu sync.RWMutex

	// collector channels
	candidateChan   <-chan *block.Block
	certificateChan <-chan certMsg
}

// New returns a new chain object
func New(eventBus *wire.EventBus, rpcBus *wire.RPCBus, c committee.Foldable) (*Chain, error) {
	drvr, err := database.From(cfg.Get().Database.Driver)
	if err != nil {
		return nil, err
	}

	db, err := drvr.Open(cfg.Get().Database.Dir, protocol.MagicFromConfig(), false)
	if err != nil {
		return nil, err
	}

	l, err := newLoader(db)
	if err != nil {
		return nil, fmt.Errorf("failure %s on loading chain '%s'", err.Error(), cfg.Get().Database.Dir)
	}

	// set up collectors
	candidateChan := initBlockCollector(eventBus, string(topics.Candidate))
	certificateChan := initCertificateCollector(eventBus)

	// set up committee
	if c == nil {
		c = committee.NewAgreement(eventBus)
	}

	chain := &Chain{
		eventBus:        eventBus,
		rpcBus:          rpcBus,
		db:              db,
		committee:       c,
		prevBlock:       *l.chainTip,
		candidateChan:   candidateChan,
		certificateChan: certificateChan,
	}

	eventBus.SubscribeCallback(string(topics.Block), chain.onAcceptBlock)
	eventBus.RegisterPreprocessor(string(topics.Candidate), consensus.NewRepublisher(eventBus, topics.Candidate))
	return chain, nil
}

// Listen to the collectors
func (c *Chain) Listen() {
	for {
		select {

		case b := <-c.candidateChan:
			_ = c.handleCandidateBlock(*b)
		case cMsg := <-c.certificateChan:
			c.addCertificate(cMsg.hash, cMsg.cert)

		// wire.RPCBus requests handlers
		case r := <-wire.GetLastBlockChan:

			buf := new(bytes.Buffer)

			c.mu.RLock()
			_ = c.prevBlock.Encode(buf)
			c.mu.RUnlock()

			r.RespChan <- *buf
		}
	}
}

func (c *Chain) propagateBlock(blk block.Block) error {
	buffer := new(bytes.Buffer)
	if err := blk.Encode(buffer); err != nil {
		return err
	}

	msg, err := wire.AddTopic(buffer, topics.Block)
	if err != nil {
		return err
	}

	c.eventBus.Stream(string(topics.Gossip), msg)
	return nil
}

func (c *Chain) addProvisioner(tx *transactions.Stake, startHeight uint64) error {
	buffer := bytes.NewBuffer(tx.PubKeyEd)
	if err := encoding.WriteVarBytes(buffer, tx.PubKeyBLS); err != nil {
		return err
	}

	totalAmount := getTxTotalOutputAmount(tx)
	if err := encoding.WriteUint64(buffer, binary.LittleEndian, totalAmount); err != nil {
		return err
	}

	if err := encoding.WriteUint64(buffer, binary.LittleEndian, startHeight); err != nil {
		return err
	}

	c.eventBus.Publish(msg.NewProvisionerTopic, buffer)
	return nil
}

func (c *Chain) addBidder(tx *transactions.Bid) error {
	totalAmount := getTxTotalOutputAmount(tx)
	x := calculateX(totalAmount, tx.M)
	bids := &user.BidList{}
	bids.AddBid(x)

	c.propagateBidList(bids)
	return nil
}

func (c *Chain) propagateBidList(bids *user.BidList) {
	var bidListBytes []byte
	for _, bid := range *bids {
		bidListBytes = append(bidListBytes, bid[:]...)
	}

	c.eventBus.Publish(msg.BidListTopic, bytes.NewBuffer(bidListBytes))
}

func (c *Chain) Close() error {

	log.Info("Close database")

	drvr, err := database.From(cfg.Get().Database.Driver)
	if err != nil {
		return err
	}

	return drvr.Close()
}

func getTxTotalOutputAmount(tx transactions.Transaction) (totalAmount uint64) {
	for _, output := range tx.StandardTX().Outputs {
		amount := big.NewInt(0).SetBytes(output.Commitment).Uint64()
		totalAmount += amount
	}

	return
}

func calculateX(d uint64, m []byte) user.Bid {
	dScalar := ristretto.Scalar{}
	dScalar.SetBigInt(big.NewInt(0).SetUint64(d))

	mScalar := ristretto.Scalar{}
	mScalar.UnmarshalBinary(m)

	x := zkproof.CalculateX(dScalar, mScalar)

	var bid user.Bid
	copy(bid[:], x.Bytes()[:])
	return bid
}

func (c *Chain) onAcceptBlock(m *bytes.Buffer) error {
	blk := block.NewBlock()
	if err := blk.Decode(m); err != nil {
		return err
	}

	return c.AcceptBlock(*blk)
}

// AcceptBlock will accept a block if
// 1. We have not seen it before
// 2. All stateless and statefull checks are true
// Returns nil, if checks passed and block was successfully saved
func (c *Chain) AcceptBlock(blk block.Block) error {

	c.mu.Lock()
	defer c.mu.Unlock()

	field := logger.Fields{"process": "accept block"}
	l := log.WithFields(field)

	l.Trace("procedure started")

	// 1. Check that stateless and stateful checks pass
	if err := verifiers.CheckBlock(c.db, c.prevBlock, blk); err != nil {
		l.Errorf("verification failed: %s", err.Error())
		return err
	}

	// 2. Check the certificate
	if err := verifiers.CheckBlockCertificate(c.committee, blk); err != nil {
		l.Errorf("verifying the certificate failed: %s", err.Error())
		return err
	}

	// 3. Add provisioners and block generators
	c.addConsensusNodes(blk.Txs, blk.Header.Height+1)

	// 4. Store block in database
	err := c.db.Update(func(t database.Transaction) error {
		return t.StoreBlock(&blk)
	})

	if err != nil {
		l.Errorf("block storing failed: %s", err.Error())
		return err
	}

	c.prevBlock = blk

	// 5. Notify other subsystems for the accepted block
	// Subsystems listening for this topic:
	// mempool.Mempool
	// consensus.generation.broker
	buf := new(bytes.Buffer)
	if err := blk.Encode(buf); err != nil {
		l.Errorf("block encoding failed: %s", err.Error())
		return err
	}

	c.eventBus.Publish(string(topics.AcceptedBlock), buf)

	// 6. Gossip advertise block Hash
	if err := c.advertiseBlock(blk); err != nil {
		l.Errorf("block advertising failed: %s", err.Error())
		return err
	}

	// 7. Cleanup obsolete candidate blocks
	var count uint32
	err = c.db.Update(func(t database.Transaction) error {
		count, err = t.DeleteCandidateBlocks(blk.Header.Height)
		return err
	})

	if err != nil {
		// Not critical enough to abort the accepting procedure
		log.Warnf("DeleteCandidateBlocks failed with an error: %s", err.Error())
	} else {
		log.Infof("%d deleted candidate blocks", count)
	}

	l.Trace("procedure ended")

	return nil
}

func (c *Chain) addConsensusNodes(txs []transactions.Transaction, provisionerStartHeight uint64) {
	field := logger.Fields{"process": "accept block"}
	l := log.WithFields(field)

	for _, tx := range txs {
		switch tx.Type() {
		case transactions.StakeType:
			stake := tx.(*transactions.Stake)
			if err := c.addProvisioner(stake, provisionerStartHeight); err != nil {
				l.Errorf("adding provisioner failed: %s", err.Error())
			}
		case transactions.BidType:
			bid := tx.(*transactions.Bid)
			if err := c.addBidder(bid); err != nil {
				l.Errorf("adding bidder failed: %s", err.Error())
			}
		}
	}
}

func (c *Chain) handleCandidateBlock(candidate block.Block) error {
	// Ensure the candidate block satisfies all chain rules
	c.mu.RLock()
	if err := verifiers.CheckBlock(c.db, c.prevBlock, candidate); err != nil {
		log.Errorf("verifying the candidate block failed: %s", err.Error())
		c.mu.RUnlock()
		return err
	}
	c.mu.RUnlock()

	// Save it into persistent storage
	err := c.db.Update(func(t database.Transaction) error {
		return t.StoreCandidateBlock(&candidate)
	})

	if err != nil {
		log.Errorf("storing the candidate block failed: %s", err.Error())
		return err
	}

	return nil
}

func (c *Chain) addCertificate(blockHash []byte, cert *block.Certificate) {
	var candidate *block.Block
	err := c.db.View(func(t database.Transaction) error {
		var err error
		candidate, err = t.FetchCandidateBlock(blockHash)
		return err
	})

	if err != nil {
		log.Errorf("error fetching candidate block to add certificate: %s", err.Error())
		return
	}

	candidate.Header.Certificate = cert
	c.AcceptBlock(*candidate)
}

// Send Inventory message to all peers
func (c *Chain) advertiseBlock(b block.Block) error {
	msg := &peermsg.Inv{}
	msg.AddItem(peermsg.InvTypeBlock, b.Header.Hash)

	buf := new(bytes.Buffer)
	if err := msg.Encode(buf); err != nil {
		panic(err)
	}

	withTopic, err := wire.AddTopic(buf, topics.Inv)
	if err != nil {
		return err
	}

	c.eventBus.Stream(string(topics.Gossip), withTopic)
	return nil
}
