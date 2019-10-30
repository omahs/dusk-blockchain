package candidate

import (
	"bytes"
	"math/big"
	"time"

	"github.com/bwesterb/go-ristretto"
	"github.com/dusk-network/dusk-blockchain/pkg/config"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/generation"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/selection"
	"github.com/dusk-network/dusk-blockchain/pkg/core/marshalling"
	"github.com/dusk-network/dusk-blockchain/pkg/util/nativeutils/eventbus"
	"github.com/dusk-network/dusk-blockchain/pkg/util/nativeutils/rpcbus"
	"github.com/dusk-network/dusk-wallet/block"

	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/encoding"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/topics"
	"github.com/dusk-network/dusk-wallet/key"
	"github.com/dusk-network/dusk-wallet/transactions"
	log "github.com/sirupsen/logrus"
)

var _ consensus.Component = (*Generator)(nil)

var lg *log.Entry = log.WithField("process", "candidate generator")

type Generator struct {
	publisher eventbus.Publisher
	// generator Public Keys to sign the rewards tx
	genPubKey *key.PublicKey
	rpcBus    *rpcbus.RPCBus
	signer    consensus.Signer

	roundInfo consensus.RoundUpdate
}

func NewComponent(publisher eventbus.Publisher, genPubKey *key.PublicKey, rpcBus *rpcbus.RPCBus) *Generator {
	return &Generator{
		publisher: publisher,
		rpcBus:    rpcBus,
		genPubKey: genPubKey,
	}
}

func (bg *Generator) Initialize(eventPlayer consensus.EventPlayer, signer consensus.Signer, ru consensus.RoundUpdate) []consensus.TopicListener {
	bg.roundInfo = ru
	bg.signer = signer

	scoreEventListener := consensus.TopicListener{
		Topic:    topics.ScoreEvent,
		Listener: consensus.NewSimpleListener(bg.Collect, consensus.LowPriority),
	}

	return []consensus.TopicListener{scoreEventListener}
}

// Finalize implements consensus.Component
func (bg *Generator) Finalize() {}

func (bg *Generator) Collect(e consensus.Event) error {
	sev := &generation.ScoreEvent{}
	if err := generation.Unmarshal(&e.Payload, sev); err != nil {
		return err
	}

	blk, err := bg.Generate(*sev)
	if err != nil {
		return err
	}

	score := &selection.Score{
		Score:         sev.Proof.Score,
		Proof:         sev.Proof.Proof,
		Z:             sev.Proof.Z,
		BidListSubset: sev.Proof.BinaryBidList,
		PrevHash:      bg.roundInfo.Hash,
		Seed:          sev.Seed,
		VoteHash:      blk.Header.Hash,
	}

	scoreBuf := new(bytes.Buffer)
	if err := selection.MarshalScore(scoreBuf, score); err != nil {
		return err
	}

	lg.Debugln("sending score")
	if err := bg.signer.SendAuthenticated(topics.Score, blk.Header.Hash, scoreBuf); err != nil {
		return err
	}

	buf := new(bytes.Buffer)
	if err := marshalling.MarshalBlock(buf, blk); err != nil {
		return err
	}

	lg.Debugln("sending candidate")
	return bg.signer.SendAuthenticated(topics.Candidate, blk.Header.Hash, buf)
}

func (bg *Generator) Generate(sev generation.ScoreEvent) (*block.Block, error) {
	return bg.GenerateBlock(bg.roundInfo.Round, sev.Seed, sev.Proof.Proof, sev.Proof.Score, bg.roundInfo.Hash)
}

func (bg *Generator) GenerateBlock(round uint64, seed, proof, score, prevBlockHash []byte) (*block.Block, error) {
	txs, err := bg.ConstructBlockTxs(proof, score)
	if err != nil {
		return nil, err
	}

	// Construct header
	h := &block.Header{
		Version:       0,
		Timestamp:     time.Now().Unix(),
		Height:        round,
		PrevBlockHash: prevBlockHash,
		TxRoot:        nil,
		Seed:          seed,
		Certificate:   block.EmptyCertificate(),
	}

	// Construct the candidate block
	candidateBlock := &block.Block{
		Header: h,
		Txs:    txs,
	}

	// Update TxRoot
	if err := candidateBlock.SetRoot(); err != nil {
		return nil, err
	}

	// Generate the block hash
	if err := candidateBlock.SetHash(); err != nil {
		return nil, err
	}

	return candidateBlock, nil
}

func (bg *Generator) ConstructBlockTxs(proof, score []byte) ([]transactions.Transaction, error) {

	txs := make([]transactions.Transaction, 0)

	// Construct and append coinbase Tx to reward the generator
	coinbaseTx, err := bg.constructCoinbaseTx(bg.genPubKey, proof, score)
	if err != nil {
		return nil, err
	}

	txs = append(txs, coinbaseTx)

	// Retrieve and append the verified transactions from Mempool
	if bg.rpcBus != nil {
		r, err := bg.rpcBus.Call(rpcbus.GetMempoolTxs, rpcbus.NewRequest(bytes.Buffer{}), 4*time.Second)
		// TODO: GetVerifiedTxs should ensure once again that none of the txs have been
		// already accepted in the chain.
		if err != nil {
			return nil, err
		}

		lTxs, err := encoding.ReadVarInt(&r)
		if err != nil {
			return nil, err
		}

		for i := uint64(0); i < lTxs; i++ {
			tx, err := marshalling.UnmarshalTx(&r)
			if err != nil {
				return nil, err
			}

			txs = append(txs, tx)
		}
	}

	// TODO Append Provisioners rewards

	return txs, nil
}

// constructCoinbaseTx forges the transactions to reward the block generator
func (bg *Generator) constructCoinbaseTx(rewardReceiver *key.PublicKey, proof []byte, score []byte) (*transactions.Coinbase, error) {
	// The rewards for both the Generator and the Provisioners are disclosed.
	// Provisioner reward addresses do not require obfuscation
	// The Generator address rewards do.

	// Construct one-time address derived from block generator public key
	// the big random number to be used in calculating P and R
	var r ristretto.Scalar
	r.Rand()

	// Create transaction
	tx := transactions.NewCoinbase(proof, score, 2)

	// Set r to our generated value
	tx.SetTxPubKey(r)

	// Disclose  reward
	var reward ristretto.Scalar
	reward.SetBigInt(big.NewInt(int64(config.GeneratorReward)))

	// Store the reward in the coinbase tx
	tx.AddReward(*rewardReceiver, reward)

	// TODO: Optional here could be to verify if the reward is spendable by the generator wallet.
	// This could be achieved with a request to dusk-wallet

	return tx, nil
}
