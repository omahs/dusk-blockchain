package consensus

import (
	"bytes"
	"fmt"
	"math/rand"

	"github.com/dusk-network/dusk-blockchain/pkg/core/consensus/header"
	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/topics"
	"github.com/dusk-network/dusk-blockchain/pkg/util/nativeutils/eventbus"
)

type Priority uint8

const (
	HighPriority Priority = iota
	LowPriority
)

// Signer encapsulate the credentials to sign or authenticate outgoing events
type Signer interface {
	Sign([]byte, []byte) ([]byte, error)
	SendAuthenticated(topics.Topic, []byte, *bytes.Buffer) error
	SendWithHeader(topics.Topic, []byte, *bytes.Buffer) error
}

type EventPlayer interface {
	Forward()
	Pause(uint32)
	Resume(uint32)
}

// ComponentFactory holds the data to create a Component (i.e. Signer, EventPublisher, RPCBus). Its responsibility is to recreate it on demand
type ComponentFactory interface {
	// Instantiate a new Component without initializing it
	Instantiate() Component
}

// Component is an ephemeral instance that lives solely for a round
type Component interface {
	// Initialize a Component with data relevant to the current Round
	Initialize(EventPlayer, Signer, RoundUpdate) []TopicListener
	// Finalize allows a Component to perform cleanup operations before begin garbage collected
	Finalize()
}

// Listener subscribes to the Coordinator and forwards consensus events to the components
type Listener interface {
	// NotifyPayload forwards consensus events to the component
	NotifyPayload(Event) error
	// ID is used to later unsubscribe from the Coordinator. This is useful for components active throughout
	// multiple steps
	ID() uint32
	Priority() Priority
}

// SimpleListener implements Listener and uses a callback for notifying events
type SimpleListener struct {
	callback func(Event) error
	id       uint32
	priority Priority
}

// NewSimpleListener creates a SimpleListener
func NewSimpleListener(callback func(Event) error, priority Priority) Listener {
	id := rand.Uint32()
	return &SimpleListener{callback, id, priority}
}

// NotifyPayload triggers the callback specified during instantiation
func (s *SimpleListener) NotifyPayload(ev Event) error {
	return s.callback(ev)
}

// ID returns the id to allow Component to unsubscribe
func (s *SimpleListener) ID() uint32 {
	return s.id
}

func (s *SimpleListener) Priority() Priority {
	return s.priority
}

// FilteringListener is a Listener that performs filtering before triggering the callback specified by the component
// Normally it is used to filter out events sent by Provisioners not being part of a committee or invalid messages.
// Filtering is applied to the `header.Header`
type FilteringListener struct {
	*SimpleListener
	filter func(header.Header) bool
}

// NewFilteringListener creates a FilteringListener
func NewFilteringListener(callback func(Event) error, filter func(header.Header) bool, priority Priority) Listener {
	id := rand.Uint32()
	return &FilteringListener{&SimpleListener{callback, id, priority}, filter}
}

// NotifyPayload uses the filtering function to let only relevant events through
func (cb *FilteringListener) NotifyPayload(ev Event) error {
	if cb.filter(ev.Header) {
		return fmt.Errorf("event has been filtered and won't be forwarded to the component - round: %d / step: %d", ev.Header.Round, ev.Header.Step)
	}
	return cb.SimpleListener.NotifyPayload(ev)
}

// TopicListener is Listener carrying a Topic
type TopicListener struct {
	Listener
	Preprocessors []eventbus.Preprocessor
	Topic         topics.Topic
	Paused        bool
}
