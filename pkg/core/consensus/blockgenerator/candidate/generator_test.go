// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT License was not distributed with this
// file, you can obtain one at https://opensource.org/licenses/MIT.
//
// Copyright (c) DUSK NETWORK. All rights reserved.

package candidate_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus"
	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/blockgenerator/candidate"
	"github.com/dusk-network/dusk-blockchain/pkg/core/data/ipc/transactions"
	"github.com/stretchr/testify/require"
)

func TestGenerate(t *testing.T) {
	if _, present := os.LookupEnv("USE_OLDBLOCKS"); !present {
		t.Skip()
	}

	hlp := candidate.NewHelper(50, time.Second)

	fn := func(ctx context.Context, txs []transactions.ContractCall, h uint64, gaslimit uint64, generator []byte) ([]transactions.ContractCall, []byte, error) {
		return []transactions.ContractCall{transactions.RandTx()}, make([]byte, 32), nil
	}

	gen := candidate.New(hlp.Emitter, fn)

	ctx := context.Background()

	ru := consensus.MockRoundUpdate(uint64(1), hlp.P)
	_, err := gen.GenerateCandidateMessage(ctx, ru, uint8(1))
	require.NoError(t, err)
}
