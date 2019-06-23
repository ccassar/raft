package raft

import (
	"context"
)

// Generic events... note how we carry *all* the context in the event; i.e. when we produce
// an event we know all the context necessary to dispose of the event. This is useful in the flow
// between the raftEngine and gRPC clients (which are not smart and simply push whatever message
// they have been given).
type event interface {
	handle(ctx context.Context)
	// Used to generate consistent k/v for logging.
	logKV() []interface{}
}

// eventFlushUndo is a wrapper event carrying another event, and which decrements the flush atomic
// counter on the client. The effect of the latter operation is typically to make the client stop
// discarding events (or get the client closer to that point).
type eventFlushUndo struct {
	client       *raftClient
	wrappedEvent event
}

func (e *eventFlushUndo) handle(ctx context.Context) {
	// Decrement flush, and invoke original event.
	e.client.updateFlush(false)

	if e.wrappedEvent != nil {
		// Note... we may still be in discard mode, because some subsequent event enqueues also requested discards.
		// In that case inner handler will correctly discard.
		e.wrappedEvent.handle(ctx)
	}
}

func (e *eventFlushUndo) logKV() []interface{} {
	return append(append([]interface{}{"obj", "requestFlushUndo(wrapper)"}, e.wrappedEvent.logKV()...),
		e.client.logKV()...)
}

type requestVoteEvent struct {
	client    *raftClient
	container requestVoteContainer
}

func (e *requestVoteEvent) handle(ctx context.Context) {

	if e.client.discardEligibleEvent() {
		return // client in flush mode.
	}

	e.container.reply, e.container.err = e.client.grpcClient.RequestVote(ctx, e.container.request)
	select {
	case e.container.returnChan <- &e.container:
	case <-ctx.Done():
		e.client.node.logger.Debugw("request vote discarded, shutting down", e.logKV()...)
	}
}

func (e *requestVoteEvent) logKV() []interface{} {
	return append([]interface{}{"obj", "requestVoteEvent", "request", *e.container.request}, e.client.logKV()...)
}

type appendEntryEvent struct {
	client    *raftClient
	container appendEntryContainer
}

func (e *appendEntryEvent) handle(ctx context.Context) {

	if e.client.discardEligibleEvent() {
		return // client in flush mode.
	}

	e.container.reply, e.container.err = e.client.grpcClient.AppendEntry(ctx, e.container.request)
	select {
	case e.container.returnChan <- &e.container:
	case <-ctx.Done():
		e.client.node.logger.Debugw("request vote discarded, shutting down", e.logKV()...)
	}
}

func (e *appendEntryEvent) logKV() []interface{} {
	return append([]interface{}{"obj", "appendEntryEvent", "request", *e.container.request}, e.client.logKV()...)
}
