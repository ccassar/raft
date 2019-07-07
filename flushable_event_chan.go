package raft

import (
	"context"
	"go.uber.org/atomic"
)

type flushableEventChannel struct {
	channel chan event
	//
	// The following field is the one field which can be written from both sides of the channel - specifically
	// incremented and, exceptionally, decremented on the producer side and more commonly decremented on the consumer
	// side. We use a lock free atomic operation to set and clear flush. Any nonzero values for flush results in a noop
	// on the consumer side (except for flush undo events).
	flush *atomic.Int32
}

// Figure out whether an event (eligible for discard) should be discarded. Only events not eligible for discard
// are wrapper events which manage flush (i.e. eventFlushUndo).
func (fec *flushableEventChannel) discardEligibleEvent() bool {
	return fec.flush.Load() != 0
}

//
// Increment/Decrement flush on flushable event channel. This can be called from both side of channel.
func (fec *flushableEventChannel) updateFlush(up bool) {
	if up {
		fec.flush.Inc()
	} else {
		fec.flush.Dec()
	}
}

// Post a message to the client indicating whether we succeeded or not. This is a non-blocking call. If the clients
// channel is full we will not block and hang around, but return with an indication that we failed to pass on the
// event.
func (fec *flushableEventChannel) postMessage(ctx context.Context, e event) bool {

	select {
	case fec.channel <- e:
	default:
		// we will indicate we failed to send if channel is full. Based on the nature of the event,
		// caller will handle how to recover. Note that this makes this function completely non blocking.
		return false
	}

	return true
}

// Post a message to a client. The message posted this way, will cause all discard eligible messages ahead in the channel
// to be discarded. If the channel is populated, then we may even drain some inline.
func (fec *flushableEventChannel) postMessageWithFlush(ctx context.Context, e event) {

	// LeaderId this point on, client starts to discard messages.
	fec.flush.Inc()
	wrapper := &eventFlushUndo{wrappedEvent: e, fec: fec}

	for {
		select {
		case fec.channel <- wrapper:
			return
		case discardEvent := <-fec.channel:
			discardEvent.handle(ctx)
		}
	}
}

func NewFlushableEventChannel(size int32) flushableEventChannel {

	fec := flushableEventChannel{
		channel: make(chan event, size),
		flush:   atomic.NewInt32(0),
	}

	return fec
}
