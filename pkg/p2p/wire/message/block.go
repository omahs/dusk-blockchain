package message

import (
	"bytes"
	"errors"
	"math"

	"github.com/dusk-network/dusk-blockchain/pkg/core/data/block"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/transactions"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/encoding"
)

// MarshalBlock marshals a block into a binary buffer
func MarshalBlock(r *bytes.Buffer, b *block.Block) error {
	if err := MarshalHeader(r, b.Header); err != nil {
		return err
	}

	lenTxs := uint64(len(b.Txs))
	if err := encoding.WriteVarInt(r, lenTxs); err != nil {
		return err
	}

	// TODO: parallelize transaction serialization
	for _, tx := range b.Txs {
		if err := MarshalTx(r, tx); err != nil {
			return err
		}
	}

	return nil
}

// UnmarshalBlock unmarshals a block from a binary buffer
func UnmarshalBlock(r *bytes.Buffer, b *block.Block) error {
	return unmarshalBlockTxs(r, b, transactions.Unmarshal)
}

type unmarfunc func(*bytes.Buffer, transactions.ContractCall) error

func unmarshalBlockTxs(r *bytes.Buffer, b *block.Block, unmarshalTx unmarfunc) error {

	if err := UnmarshalHeader(r, b.Header); err != nil {
		return err
	}

	lTxs, err := encoding.ReadVarInt(r)
	if err != nil {
		return err
	}

	// Maximum amount of transactions we can decode at once is
	// math.MaxInt32 / 8, since they are pointers (uint64)
	if lTxs > (math.MaxInt32 / 8) {
		return errors.New("block tx count too large")
	}

	b.Txs = make([]transactions.ContractCall, lTxs)
	for i, c := range b.Txs {
		err := unmarshalTx(r, c)
		if err != nil {
			return err
		}
		b.Txs[i] = c
	}

	return nil
}

//MarshalHashable marshals the hashable part of the block into a binary buffer
func MarshalHashable(r *bytes.Buffer, h *block.Header) error {
	if err := encoding.WriteUint8(r, h.Version); err != nil {
		return err
	}

	if err := encoding.WriteUint64LE(r, h.Height); err != nil {
		return err
	}

	if err := encoding.WriteUint64LE(r, uint64(h.Timestamp)); err != nil {
		return err
	}

	if err := encoding.Write256(r, h.PrevBlockHash); err != nil {
		return err
	}

	if err := encoding.WriteBLS(r, h.Seed); err != nil {
		return err
	}

	return nil
}

// MarshalHeader marshals the header of a block into a binary buffer
func MarshalHeader(r *bytes.Buffer, h *block.Header) error {
	if err := MarshalHashable(r, h); err != nil {
		return err
	}

	if err := encoding.Write256(r, h.TxRoot); err != nil {
		return err
	}

	if err := MarshalCertificate(r, h.Certificate); err != nil {
		return err
	}

	if err := encoding.Write256(r, h.Hash); err != nil {
		return err
	}

	return nil
}

// UnmarshalHeader unmarshal a block header from a binary buffer
func UnmarshalHeader(r *bytes.Buffer, h *block.Header) error {
	if err := encoding.ReadUint8(r, &h.Version); err != nil {
		return err
	}

	if err := encoding.ReadUint64LE(r, &h.Height); err != nil {
		return err
	}

	var timestamp uint64
	if err := encoding.ReadUint64LE(r, &timestamp); err != nil {
		return err
	}
	h.Timestamp = int64(timestamp)

	h.PrevBlockHash = make([]byte, 32)
	if err := encoding.Read256(r, h.PrevBlockHash); err != nil {
		return err
	}

	h.Seed = make([]byte, 33)
	if err := encoding.ReadBLS(r, h.Seed); err != nil {
		return err
	}

	h.TxRoot = make([]byte, 32)
	if err := encoding.Read256(r, h.TxRoot); err != nil {
		return err
	}

	if err := UnmarshalCertificate(r, h.Certificate); err != nil {
		return err
	}

	h.Hash = make([]byte, 32)
	if err := encoding.Read256(r, h.Hash); err != nil {
		return err
	}

	return nil
}

// MarshalCertificate marshals a certificate
func MarshalCertificate(r *bytes.Buffer, c *block.Certificate) error {
	if err := encoding.WriteBLS(r, c.StepOneBatchedSig); err != nil {
		return err
	}

	if err := encoding.WriteBLS(r, c.StepTwoBatchedSig); err != nil {
		return err
	}

	if err := encoding.WriteUint8(r, c.Step); err != nil {
		return err
	}

	if err := encoding.WriteUint64LE(r, c.StepOneCommittee); err != nil {
		return err
	}

	if err := encoding.WriteUint64LE(r, c.StepTwoCommittee); err != nil {
		return err
	}

	return nil
}

// UnmarshalCertificate unmarshals a certificate
func UnmarshalCertificate(r *bytes.Buffer, c *block.Certificate) error {
	c.StepOneBatchedSig = make([]byte, 33)
	if err := encoding.ReadBLS(r, c.StepOneBatchedSig); err != nil {
		return err
	}

	c.StepTwoBatchedSig = make([]byte, 33)
	if err := encoding.ReadBLS(r, c.StepTwoBatchedSig); err != nil {
		return err
	}

	if err := encoding.ReadUint8(r, &c.Step); err != nil {
		return err
	}

	if err := encoding.ReadUint64LE(r, &c.StepOneCommittee); err != nil {
		return err
	}

	if err := encoding.ReadUint64LE(r, &c.StepTwoCommittee); err != nil {
		return err
	}

	return nil
}
