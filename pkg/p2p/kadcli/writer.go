// This Source Code Form is subject to the terms of the MIT License.
// If a copy of the MIT License was not distributed with this
// file, you can obtain one at https://opensource.org/licenses/MIT.
//
// Copyright (c) DUSK NETWORK. All rights reserved.

package kadcli

import (
	"context"
	"errors"

	"github.com/dusk-network/dusk-blockchain/pkg/p2p/wire/topics"
	"github.com/dusk-network/dusk-blockchain/pkg/util/nativeutils/eventbus"
	"github.com/dusk-network/dusk-protobuf/autogen/go/rusk"
)

// Writer abstracts all of the logic and fields needed to write messages to
// other network nodes.
type Writer struct {
	subscriber eventbus.Subscriber

	kadcastSubscription      uint32
	kadcastPointSubscription uint32

	cli rusk.NetworkClient
}

// NewWriter returns a Writer. It will still need to be initialized by
// subscribing to the gossip topic with a stream handler, and by running the WriteLoop
// in a goroutine.
func NewWriter(s eventbus.Subscriber, rusk rusk.NetworkClient) *Writer {
	return &Writer{
		subscriber: s,
		cli:        rusk,
	}
}

// Serve subscribes to eventbus Kadcast messages and injects the writer.
func (w *Writer) Serve() {
	// Kadcast subs
	l1 := eventbus.NewStreamListener(w)
	w.kadcastSubscription = w.subscriber.Subscribe(topics.Kadcast, l1)
	// KadcastPoint subs
	l2 := eventbus.NewStreamListener(w)
	w.kadcastPointSubscription = w.subscriber.Subscribe(topics.KadcastPoint, l2)
}

// Write sends a message through the Kadcast gRPC interface.
// Note: Assumes the message is properly encoded (no pre-processing done here)
func (w *Writer) Write(data, header []byte, priority byte) (int, error) {
	// check header
	if len(header) == 0 {
		return 0, errors.New("empty message header")
	}
	// send
	go func() {
		var err error
		// send a p2p message
		if len(header) > 1 {
			err = w.WriteToPoint(data, header, priority)
		}
		// broadcast a message
		if len(header) == 1 {
			err = w.WriteToAll(data, header, priority)
		}
		// log errors
		if err != nil {
			log.WithError(err).Warn("write failed")
		}
	}()
	return 0, nil
}

// WriteToAll broadcasts message to the entire network.
// The kadcast height is read from message Header.
// Note: Assumes the message is properly encoded (no pre-processing done here)
func (w *Writer) WriteToAll(data, header []byte, priority byte) error {
	// check header
	if len(header) == 0 {
		return errors.New("empty message header")
	}
	// exctract kadcast height
	height := header[0]
	// broadcast
	return w.broadcastPacket(height, data)
}

// WriteToPoint writes a message to a single destination.
// The receiver address is read from message Header.
// Note: Assumes the message is properly encoded (no pre-processing done here)
func (w *Writer) WriteToPoint(data, header []byte, priority byte) error {
	// check header
	if len(header) == 0 {
		return errors.New("empty message header")
	}
	// extract destination address
	addr := string(header)
	return w.sendPacket(addr, data)
}

// BroadcastPacket passes a message to the kadkast peer to be broadcasted.
func (w *Writer) broadcastPacket(maxHeight byte, payload []byte) error {
	h := uint32(maxHeight)
	m := &rusk.BroadcastMessage{
		KadcastHeight: h,
		Message:       payload,
	}
	// broadcast message
	if _, err := w.cli.Broadcast(context.TODO(), m); err != nil {
		log.WithError(err).Warn("failed to broadcast message")
		return err
	}
	return nil
}

// sendPacket passes a message to the kadkast peer to be sent to a peer.
func (w *Writer) sendPacket(addr string, payload []byte) error {
	m := &rusk.SendMessage{
		TargetAddress: addr,
		Message:       payload,
	}
	if _, err := w.cli.Send(context.TODO(), m); err != nil {
		log.WithError(err).Warn("failed to broadcast message")
		return err
	}
	return nil
}

// Close unsubscribes from eventbus events.
func (w *Writer) Close() error {
	w.subscriber.Unsubscribe(topics.Kadcast, w.kadcastSubscription)
	w.subscriber.Unsubscribe(topics.KadcastPoint, w.kadcastPointSubscription)
	return nil
}
