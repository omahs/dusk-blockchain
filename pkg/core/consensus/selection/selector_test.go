package selection_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"gitlab.dusk.network/dusk-core/dusk-go/mocks"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/header"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/msg"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/selection"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/consensus/user"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/core/tests/helper"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/crypto"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire"
	"gitlab.dusk.network/dusk-core/dusk-go/pkg/p2p/wire/topics"
)

func TestSelection(t *testing.T) {
	eb := wire.NewEventBus()
	selection.Launch(eb, newMockScoreHandler(), time.Millisecond*200)
	// subscribe to receive a result
	bestScoreChan := make(chan *bytes.Buffer, 1)
	eb.Subscribe(msg.BestScoreTopic, bestScoreChan)

	// Update round to start the selector
	consensus.UpdateRound(eb, 1)

	sendMockEvent(eb)
	sendMockEvent(eb)
	sendMockEvent(eb)

	// we should receive something on the bestScoreChan after timeout
	ev := <-bestScoreChan
	assert.NotNil(t, ev)
}

func TestRepropagation(t *testing.T) {
	eb, streamer := helper.CreateGossipStreamer()
	selection.Launch(eb, newMockScoreHandler(), time.Millisecond*200)
	// Update round to start the selector
	consensus.UpdateRound(eb, 1)
	sendMockEvent(eb)

	timer := time.AfterFunc(500*time.Millisecond, func() {
		t.Fail()
	})

	buf, err := streamer.Read()
	if err != nil {
		t.Fatal(err)
	}

	assert.True(t, len(buf) > 0)
	// Test is finished, stop the timer
	timer.Stop()
}

func TestStopSelector(t *testing.T) {
	eb := wire.NewEventBus()
	selection.Launch(eb, newMockScoreHandler(), time.Second*1)
	// subscribe to receive a result
	bestScoreChan := make(chan *bytes.Buffer, 2)
	eb.Subscribe(msg.BestScoreTopic, bestScoreChan)

	// Update round to start the selector
	consensus.UpdateRound(eb, 1)
	sendMockEvent(eb)
	sendMockEvent(eb)
	sendMockEvent(eb)

	// Update round again to stop the selector
	consensus.UpdateRound(eb, 2)

	timer := time.After(200 * time.Millisecond)
	select {
	case <-bestScoreChan:
		assert.FailNow(t, "Selector should have not returned a value")
	case <-timer:
		// success :)
	}
}

func sendMockEvent(eb *wire.EventBus) {
	eb.Publish(string(topics.Score), bytes.NewBuffer([]byte("foo")))
}

type mockScoreHandler struct {
	consensus.EventHandler
}

func newMockScoreHandler() selection.ScoreEventHandler {
	return &mockScoreHandler{
		EventHandler: newMockHandler(),
	}
}

func (m *mockScoreHandler) Priority(ev1, ev2 wire.Event) bool {
	return false
}

func (m *mockScoreHandler) Marshal(b *bytes.Buffer, ev wire.Event) error {
	_, err := b.Write([]byte("foo"))
	return err
}

func (m *mockScoreHandler) UpdateBidList(bL user.BidList) {}
func (m *mockScoreHandler) LowerThreshold()               {}
func (m *mockScoreHandler) ResetThreshold()               {}

func newMockHandler() consensus.EventHandler {
	var sender []byte
	mockEventHandler := &mocks.EventHandler{}
	mockEventHandler.On("Verify", mock.Anything).Return(nil)
	mockEventHandler.On("Marshal", mock.Anything, mock.Anything).Return(nil)
	mockEventHandler.On("Deserialize", mock.Anything).
		Return(selection.MockSelectionEvent(1, make([]byte, 32)), nil)
	mockEventHandler.On("ExtractHeader",
		mock.MatchedBy(func(ev wire.Event) bool {
			sender, _ = crypto.RandEntropy(32)
			return true
		})).Return(func(e wire.Event) *header.Header {
		return &header.Header{
			Round:     1,
			Step:      1,
			PubKeyBLS: sender,
		}
	})
	return mockEventHandler
}
