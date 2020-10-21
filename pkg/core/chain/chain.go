package chain

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/capi"

	"github.com/dusk-network/dusk-blockchain/pkg/util/diagnostics"

	"encoding/hex"

	"github.com/dusk-network/dusk-blockchain/pkg/config"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/block"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/ipc/transactions"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/peer/peermsg"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/peer/processing/chainsync"
	"github.com/dusk-network/dusk-blockchain/pkg/util/nativeutils/eventbus"
	"github.com/dusk-network/dusk-blockchain/pkg/util/nativeutils/rpcbus"
	"github.com/dusk-network/dusk-protobuf/autogen/go/node"
	logger "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/user"

	"github.com/dusk-network/dusk-blockchain/pkg/core/verifiers"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/encoding"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/message"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/topics"
)

var log = logger.WithFields(logger.Fields{"process": "chain"})

// Verifier performs checks on the blockchain and potentially new incoming block
type Verifier interface {
	// PerformSanityCheck on first N blocks and M last blocks
	PerformSanityCheck(startAt uint64, firstBlocksAmount uint64, lastBlockAmount uint64) error
	// SanityCheckBlock will verify whether a block is valid according to the rules of the consensus
	SanityCheckBlock(prevBlock block.Block, blk block.Block) error
}

// Loader is an interface which abstracts away the storage used by the Chain to
// store the blockchain
type Loader interface {
	// LoadTip of the chain
	LoadTip() (*block.Block, error)
	// Clear removes everything from the DB
	Clear() error
	// Close the Loader and finalizes any pending connection
	Close(driver string) error
	// Height returns the current height as stored in the loader
	Height() (uint64, error)
	// BlockAt returns the block at a given height
	BlockAt(uint64) (block.Block, error)
	// Append a block on the storage
	Append(*block.Block) error
}

// Chain represents the nodes blockchain
// This struct will be aware of the current state of the node.
type Chain struct {
	eventBus *eventbus.EventBus
	rpcBus   *rpcbus.RPCBus
	p        *user.Provisioners
	counter  *chainsync.Counter

	// loader abstracts away the persistence aspect of Block operations
	loader Loader

	// verifier performs verifications on the block
	verifier Verifier

	// current blockchain tip of local state
	tip *lastBlockProvider

	// Most recent certificate generated by the Agreement component.
	// Held on the Chain, to be requested by the block generator,
	// for including it with the candidate message.
	lastCertificate *block.Certificate

	// BLS keys of the most recent committee, responsible for finalizing the intermediate block.
	lastCommittee [][]byte

	// The highest block we've seen from the network. This is updated
	// by the synchronizer, and used to calculate our synchronization
	// progress.
	highestSeen uint64

	// collector channels
	certificateChan    <-chan certMsg
	highestSeenChan    <-chan uint64
	blockChan          chan message.Message
	initializationChan chan message.Message

	// rusk client
	executor transactions.Executor

	// rpcbus channels
	verifyCandidateBlockChan <-chan rpcbus.Request
	getLastCertificateChan   <-chan rpcbus.Request
	getLastCommitteeChan     <-chan rpcbus.Request

	ctx context.Context

	onBeginAccepting func(*block.Block) bool
}

// New returns a new chain object. It accepts the EventBus (for messages coming
// from (remote) consensus components, the RPCBus for dispatching synchronous
// data related to Certificates, Blocks, Rounds and progress. It also accepts a
// counter to manage the synchronization process and the hash of the genesis
// block
// TODO: the counter should be encapsulated in a specific component for
// synchronization
func New(ctx context.Context, eventBus *eventbus.EventBus, rpcBus *rpcbus.RPCBus, counter *chainsync.Counter, loader Loader, verifier Verifier, srv *grpc.Server, executor transactions.Executor) (*Chain, error) {
	// set up collectors
	certificateChan := initCertificateCollector(eventBus)
	highestSeenChan := initHighestSeenCollector(eventBus)

	// set up rpcbus channels
	verifyCandidateBlockChan := make(chan rpcbus.Request, 1)
	getLastCertificateChan := make(chan rpcbus.Request, 1)
	getLastCommitteeChan := make(chan rpcbus.Request, 1)

	if err := rpcBus.Register(topics.VerifyCandidateBlock, verifyCandidateBlockChan); err != nil {
		return nil, err
	}
	if err := rpcBus.Register(topics.GetLastCertificate, getLastCertificateChan); err != nil {
		return nil, err
	}

	if err := rpcBus.Register(topics.GetLastCommittee, getLastCommitteeChan); err != nil {
		return nil, err
	}

	chain := &Chain{
		eventBus:                 eventBus,
		rpcBus:                   rpcBus,
		p:                        user.NewProvisioners(),
		counter:                  counter,
		certificateChan:          certificateChan,
		highestSeenChan:          highestSeenChan,
		verifyCandidateBlockChan: verifyCandidateBlockChan,
		getLastCertificateChan:   getLastCertificateChan,
		getLastCommitteeChan:     getLastCommitteeChan,
		lastCommittee:            make([][]byte, 0),
		loader:                   loader,
		verifier:                 verifier,
		executor:                 executor,
		ctx:                      ctx,
	}

	chain.onBeginAccepting = chain.beginAccepting

	prevBlock, err := loader.LoadTip()
	if err != nil {
		return nil, err
	}

	if prevBlock.Header.Height == 0 {
		// TODO: maybe it would be better to have a consensus-compatible certificate.
		chain.lastCertificate = block.EmptyCertificate()

		// If we're running the test harness, we should also populate some consensus values
		if config.Get().Genesis.Legacy {
			if errV := setupBidValues(); errV != nil {
				return nil, errV
			}

			if errV := reconstructCommittee(chain.p, prevBlock); errV != nil {
				return nil, errV
			}
		}
	}

	chain.tip, err = newLastBlockProvider(rpcBus, prevBlock)
	if err != nil {
		return nil, err
	}

	if srv != nil {
		node.RegisterChainServer(srv, chain)
	}

	// Hook the chain up to the required topics
	chain.blockChan = make(chan message.Message, config.MaxInvBlocks)
	eventBus.Subscribe(topics.Block, eventbus.NewChanListener(chain.blockChan))

	chain.initializationChan = make(chan message.Message, 1)
	eventBus.Subscribe(topics.Initialization, eventbus.NewChanListener(chain.initializationChan))
	return chain, nil
}

// Listen to the collectors
func (c *Chain) Listen() {
	for {
		select {
		case m := <-c.blockChan:
			if err := c.onAcceptBlock(m); err != nil {
				log.WithError(err).WithField("topic", topics.Block.String()).Warn("Handling block failed")
			}
		case m := <-c.initializationChan:
			if err := c.onInitialization(m); err != nil {
				log.WithError(err).WithField("topic", topics.Initialization.String()).Warn("Handling initialization failed")
			}
		case certificateMsg := <-c.certificateChan:
			c.handleCertificateMessage(certificateMsg)
		case height := <-c.highestSeenChan:
			// TODO: check out if highestSeen could be out of chain
			c.highestSeen = height
		case r := <-c.verifyCandidateBlockChan:
			c.processCandidateVerificationRequest(r)
		case r := <-c.getLastCertificateChan:
			c.provideLastCertificate(r)
		case r := <-c.getLastCommitteeChan:
			c.provideLastCommittee(r)
		case <-c.ctx.Done():
			// TODO: dispose the Chain
		}
	}
}

func (c *Chain) beginAccepting(blk *block.Block) bool {

	field := logger.Fields{"process": "onAcceptBlock", "height": blk.Header.Height}
	lg := log.WithFields(field)

	// Ignore blocks from peers if we are only one behind - we are most
	// likely just about to finalize consensus.
	// TODO: we should probably just accept it if consensus was not
	// started yet

	if !c.counter.IsSyncing() {
		lg.Warn("could not accept block since we are syncing")
		return false
	}

	// If we are more than one block behind, stop the consensus
	lg.Debug("topics.StopConsensus")
	// FIXME: this call should be blocking
	errList := c.eventBus.Publish(topics.StopConsensus, message.New(topics.StopConsensus, message.EMPTY))
	diagnostics.LogPublishErrors("chain/chain.go, topics.StopConsensus", errList)

	return true
}

func (c *Chain) onAcceptBlock(m message.Message) error {

	// Accept the block
	blk := m.Payload().(block.Block)

	// Prepare component and the node for accepting new block
	if !c.onBeginAccepting(&blk) {
		return nil
	}

	field := logger.Fields{"process": "onAcceptBlock", "height": blk.Header.Height}
	lg := log.WithFields(field)

	// Accepting the block decrements the sync counter
	if err := c.AcceptBlock(c.ctx, blk); err != nil {
		lg.WithError(err).Debug("could not AcceptBlock")
		return err
	}

	c.lastCertificate = blk.Header.Certificate

	// If we are no longer syncing after accepting this block,
	// request a certificate for the second to last round.
	if !c.counter.IsSyncing() {

		// Once received, we can re-start consensus.
		// This sets off a chain of processing which goes from sending the
		// round update, to re-instantiating the consensus, to setting off
		// the first consensus loop. So, we do this in a goroutine to
		// avoid blocking other requests to the chain.
		ru := c.getRoundUpdate()
		go func() {
			if err := c.sendRoundUpdate(ru); err != nil {
				lg.WithError(err).Debug("could not sendRoundUpdate")
			}
		}()
	}

	return nil
}

// AcceptBlock will accept a block if
// 1. We have not seen it before
// 2. All stateless and stateful checks are true
// Returns nil, if checks passed and block was successfully saved
func (c *Chain) AcceptBlock(ctx context.Context, blk block.Block) error {

	field := logger.Fields{"process": "accept block", "height": blk.Header.Height}
	l := log.WithFields(field)

	l.Trace("verifying block")

	prevBlock := c.tip.Get()
	// 1. Check that stateless and stateful checks pass
	if err := c.verifier.SanityCheckBlock(prevBlock, blk); err != nil {
		l.WithError(err).Error("block verification failed")
		return err
	}

	var provisioners user.Provisioners
	var err error
	provisioners, err = c.executor.GetProvisioners(ctx)
	if err != nil {
		l.WithError(err).Error("Error in getting provisioners")
		return err
	}

	// Set provisioners list.
	// chain.provisioners and provisioners GetProvisioners result are expected to differ only on
	// first run after node restart
	c.p = &provisioners

	// 2. Check the certificate
	// This check should avoid a possible race condition between accepting two blocks
	// at the same height, as the probability of the committee creating two valid certificates
	// for the same round is negligible.
	l.Trace("verifying block certificate")
	if err = verifiers.CheckBlockCertificate(*c.p, blk); err != nil {
		l.WithError(err).Error("certificate verification failed")
		return err
	}

	// 3. Call ExecuteStateTransitionFunction
	prov_num := c.p.Set.Len()
	l.WithField("provisioners", prov_num).Info("calling ExecuteStateTransitionFunction")

	provisioners, err = c.executor.ExecuteStateTransition(ctx, blk.Txs, blk.Header.Height)
	if err != nil {
		l.WithError(err).Error("Error in executing the state transition")
		return err
	}

	// Update the provisioners as blk.Txs may bring new provisioners to the current state
	c.p = &provisioners
	c.tip.Set(&blk)

	l.WithField("provisioners", c.p.Set.Len()).
		WithField("added", c.p.Set.Len()-prov_num).
		Info("after ExecuteStateTransitionFunction")

	if config.Get().API.Enabled {
		go func() {
			store := capi.GetStormDBInstance()
			var members []*capi.Member
			for _, v := range c.p.Members {
				var stakes []capi.Stake

				for _, s := range v.Stakes {
					stake := capi.Stake{
						Amount:      s.Amount,
						StartHeight: s.StartHeight,
						EndHeight:   s.EndHeight,
					}
					stakes = append(stakes, stake)
				}

				member := capi.Member{
					PublicKeyBLS: v.PublicKeyBLS,
					Stakes:       stakes,
				}

				members = append(members, &member)
			}

			provisioner := capi.ProvisionerJSON{
				ID:      blk.Header.Height,
				Set:     c.p.Set,
				Members: members,
			}
			err := store.Save(&provisioner)
			if err != nil {
				log.Warn("Could not store provisioners on memoryDB")
			}
		}()
	}

	// 4. Store the approved block
	l.Trace("storing block in db")
	if err := c.loader.Append(&blk); err != nil {
		l.WithError(err).Error("block storing failed")
		return err
	}

	// 5. Gossip advertise block Hash
	l.Trace("gossiping block")
	if err := c.advertiseBlock(blk); err != nil {
		l.WithError(err).Error("block advertising failed")
		return err
	}

	// 6. Notify other subsystems for the accepted block
	// Subsystems listening for this topic:
	// mempool.Mempool
	// consensus.generation.broker
	l.Trace("notifying internally")

	msg := message.New(topics.AcceptedBlock, blk)
	errList := c.eventBus.Publish(topics.AcceptedBlock, msg)
	diagnostics.LogPublishErrors("chain/chain.go, topics.AcceptedBlock", errList)

	// decrement the counter
	c.counter.Decrement()

	l.Trace("procedure ended")
	return nil
}

func (c *Chain) onInitialization(message.Message) error {
	ru := c.getRoundUpdate()
	return c.sendRoundUpdate(ru)
}

func (c *Chain) sendRoundUpdate(ru consensus.RoundUpdate) error {
	log.
		WithField("round", ru.Round).
		Debug("sendRoundUpdate, topics.RoundUpdate")

	msg := message.New(topics.RoundUpdate, ru)
	errList := c.eventBus.Publish(topics.RoundUpdate, msg)
	diagnostics.LogPublishErrors("chain/chain.go, topics.RoundUpdate", errList)

	return nil
}

func (c *Chain) processCandidateVerificationRequest(r rpcbus.Request) {
	var res rpcbus.Response

	cm := r.Params.(message.Candidate)

	candidateBlock := *cm.Block
	chainTip := c.tip.Get()

	// We first perform a quick check on the Block Header and
	if err := c.verifier.SanityCheckBlock(chainTip, candidateBlock); err != nil {
		res.Err = err
		r.RespChan <- res
		return
	}

	_, err := c.executor.VerifyStateTransition(c.ctx, candidateBlock.Txs, candidateBlock.Header.Height)
	if err != nil {
		res.Err = err
		r.RespChan <- res
		return
	}

	r.RespChan <- res
}

// Send Inventory message to all peers
func (c *Chain) advertiseBlock(b block.Block) error {
	msg := &peermsg.Inv{}
	msg.AddItem(peermsg.InvTypeBlock, b.Header.Hash)

	buf := new(bytes.Buffer)
	if err := msg.Encode(buf); err != nil {
		//TODO: shall this really panic ?
		log.Panic(err)
	}

	if err := topics.Prepend(buf, topics.Inv); err != nil {
		//TODO: shall this really panic ?
		log.Panic(err)
	}

	m := message.New(topics.Inv, *buf)
	errList := c.eventBus.Publish(topics.Gossip, m)
	diagnostics.LogPublishErrors("chain/chain.go, topics.Gossip, topics.Inv", errList)

	return nil
}

func (c *Chain) handleCertificateMessage(cMsg certMsg) {
	// Set latest certificate and committee
	c.lastCertificate = cMsg.cert
	c.lastCommittee = cMsg.committee

	// Fetch new intermediate block and corresponding certificate
	//TODO: start measuring how long this takes in order to be able to see if this timeout is good or not

	params := new(bytes.Buffer)
	_ = encoding.Write256(params, cMsg.hash)
	_ = encoding.WriteBool(params, true)

	timeoutGetCandidate := time.Duration(config.Get().Timeout.TimeoutGetCandidate) * time.Second
	resp, err := c.rpcBus.Call(topics.GetCandidate, rpcbus.NewRequest(*params), timeoutGetCandidate) //20 is tmp value for further checks
	if err != nil {
		// If the we can't get the block, we will fall
		// back and catch up later.
		//FIXME: restart consensus when handleCertificateMessage flow return err
		log.
			WithError(err).
			WithField("height", c.highestSeen).
			Error("could not find winning candidate block")
		return
	}
	cm := resp.(message.Candidate)

	// Try to accept candidate block
	cm.Block.Header.Certificate = cMsg.cert
	if err := c.AcceptBlock(c.ctx, *cm.Block); err != nil {
		log.
			WithError(err).
			WithField("candidate_hash", hex.EncodeToString(cm.Block.Header.Hash)).
			WithField("candidate_height", cm.Block.Header.Height).
			Error("could not accept candidate block")
		return
	}

	// propagate round update
	ru := c.getRoundUpdate()
	go func() {
		if err := c.sendRoundUpdate(ru); err != nil {
			log.
				WithError(err).
				WithField("height", c.highestSeen).
				Error("could not sendRoundUpdate")
		}
	}()
}

func (c *Chain) getRoundUpdate() consensus.RoundUpdate {

	prevBlock := c.tip.Get()
	hdr := prevBlock.Header
	return consensus.RoundUpdate{
		Round: hdr.Height + 1,
		P:     c.p.Copy(),
		Seed:  hdr.Seed,
		Hash:  hdr.Hash,
	}
}

func (c *Chain) provideLastCertificate(r rpcbus.Request) {
	if c.lastCertificate == nil {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, errors.New("no last certificate present"))
		return
	}

	buf := new(bytes.Buffer)
	err := message.MarshalCertificate(buf, c.lastCertificate)
	r.RespChan <- rpcbus.NewResponse(*buf, err)
}

func (c *Chain) provideLastCommittee(r rpcbus.Request) {
	if c.lastCommittee == nil {
		r.RespChan <- rpcbus.NewResponse(bytes.Buffer{}, errors.New("no last committee present"))
		return
	}

	r.RespChan <- rpcbus.NewResponse(c.lastCommittee, nil)
}

// GetSyncProgress returns how close the node is to being synced to the tip,
// as a percentage value.
func (c *Chain) GetSyncProgress(ctx context.Context, e *node.EmptyRequest) (*node.SyncProgressResponse, error) {
	if c.highestSeen == 0 {
		return &node.SyncProgressResponse{Progress: 0}, nil
	}

	prevBlock := c.tip.Get()
	prevBlockHeight := prevBlock.Header.Height
	progressPercentage := (float64(prevBlockHeight) / float64(c.highestSeen)) * 100

	// Avoiding strange output when the chain can be ahead of the highest
	// seen block, as in most cases, consensus terminates before we see
	// the new block from other peers.
	if progressPercentage > 100 {
		progressPercentage = 100
	}

	return &node.SyncProgressResponse{Progress: float32(progressPercentage)}, nil
}

// RebuildChain will delete all blocks except for the genesis block,
// to allow for a full re-sync.
func (c *Chain) RebuildChain(ctx context.Context, e *node.EmptyRequest) (*node.GenericResponse, error) {

	// Halt consensus
	msg := message.New(topics.StopConsensus, nil)
	errList := c.eventBus.Publish(topics.StopConsensus, msg)
	diagnostics.LogPublishErrors("chain/chain.go, topics.StopConsensus", errList)

	// Remove EVERYTHING from the database. This includes the genesis
	// block, so we need to add it afterwards.
	if err := c.loader.Clear(); err != nil {
		return nil, err
	}

	// Note that, beyond this point, an error in reconstructing our
	// state is unrecoverable, as it deems the node totally useless.
	// Therefore, any error encountered from now on is answered by
	// a panic.
	var tipErr error
	var tip *block.Block
	tip, tipErr = c.loader.LoadTip()
	if tipErr != nil {
		log.Panic(tipErr)
	}

	c.tip.Set(tip)
	if unrecoverable := c.verifier.PerformSanityCheck(0, SanityCheckHeight, 0); unrecoverable != nil {
		log.Panic(unrecoverable)
	}

	// Reset in-memory values
	c.resetState()

	// Clear walletDB
	timeoutClearWalletDatabase := time.Duration(config.Get().Timeout.TimeoutClearWalletDatabase) * time.Second
	if _, err := c.rpcBus.Call(topics.ClearWalletDatabase, rpcbus.NewRequest(bytes.Buffer{}), timeoutClearWalletDatabase); err != nil {
		log.Panic(err)
	}

	return &node.GenericResponse{Response: "Blockchain deleted. Syncing from scratch..."}, nil
}

func (c *Chain) resetState() {
	c.p = user.NewProvisioners()
	c.lastCertificate = block.EmptyCertificate()
}
