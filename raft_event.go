package raft

import (
	"context"
	"github.com/ccassar/raft/internal/raft_pb"
	"time"
)

// Generic events... note how we carry *all* the context in the event; i.e. when we produce
// an event we know all the context necessary to dispose of the event. This is useful in the flow
// between the raftEngine and gRPC clients (which are not smart and simply push whatever message
// they have been given).
type event interface {
	// Handle is what does the business for an event. Do note, that handle is primarily called in the context
	// of the client goroutine but may be called in other contexts while discard eligible is enabled.
	handle(ctx context.Context)
	// Used to generate consistent k/v for logging.
	logKV() []interface{}
}

// eventFlushUndo is a wrapper event carrying another event, and which decrements the flush atomic
// counter on the client. The effect of the latter operation is typically to make the client stop
// discarding events (or get the client closer to that point).
type eventFlushUndo struct {
	fec          *flushableEventChannel
	wrappedEvent event
}

func (e *eventFlushUndo) handle(ctx context.Context) {
	// Decrement flush (always), and invoke original event.
	e.fec.updateFlush(false)

	if e.wrappedEvent != nil {
		// Note... we may still be in discard mode, because some subsequent event enqueues also requested discards.
		// In that case inner handler will correctly discard.
		e.wrappedEvent.handle(ctx)
	}
}

func (e *eventFlushUndo) logKV() []interface{} {
	return append(append([]interface{}{"obj", "requestFlushUndo(wrapper)"}, e.wrappedEvent.logKV()...))
}

type requestVoteEvent struct {
	client    *raftClient
	container requestVoteContainer
}

func (e *requestVoteEvent) handle(ctx context.Context) {

	if e.client.eventChan.discardEligibleEvent() {
		return // client in flush mode.
	}

	ctx, cancel := context.WithTimeout(ctx, e.client.node.config.timers.gRPCTimeout)
	defer cancel()
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
	client *raftClient
	cal    *clientStateAtLeader
}

func (e *appendEntryEvent) handle(ctx context.Context) {

	if e.client.eventChan.discardEligibleEvent() {
		return // client in flush mode.
	}

	cal := e.cal
	re := e.client.node.engine

	// We start by understanding what the latest log entry looks like... this is how far we plan to go.
	_, lastIndex, err := re.logGetLastTermAndIndex()
	if err != nil {
		return // log and signal shutdown in called function
	}

	for {

		// In this iteration, we will attempt to push up till term. Note that there will be at least one more
		// notification in the pipeline if there are other entries beyond the latest we just read.
		nextIndex := e.cal.nextIndex.Load()

		prevTerm := int64(0)
		prevSequence := int64(0)
		prevLe, err := re.logGetEntry(nextIndex - 1)
		if err != nil {
			re.node.signalFatalError(err)
			return
		}
		if prevLe != nil {
			prevSequence = prevLe.Sequence
			prevTerm = prevLe.Term
		}

		currentTerm := re.currentTerm.Load()
		// Event originated in different term; time to dicard it.
		if currentTerm != cal.termOfOrigin {
			return
		}

		request := &raft_pb.AppendEntryRequest{
			Term:           currentTerm,
			PrevLogTerm:    prevTerm,
			PrevLogIndex:   prevSequence,
			CommittedIndex: re.commitIndex.Load(),
			LeaderId:       re.node.index,
			To:             e.client.index,
		}

		// Retries, and handling of raftEngine side meta data like new Term, or need for updating
		// the committedIndex are done via raftEngine (this makes sure we do not end up with runaway
		// event handlers trying to do leader work when we are not - would that count as a Byzantine
		// failure - we would be disseminating the wrong information).
		notifyRaftEngine := func(reply *raft_pb.AppendEntryReply, err error) {
			select {
			case re.returnsAppendEntryResponsesFromClientChan <- &appendEntryResponsesFromClient{
				request: request, reply: reply, err: err}:
			case <-ctx.Done():
			}
		}

		if lastIndex >= nextIndex {
			// We will load at most batch from here on, and send that as payload. We are not precise here,
			// and may move beyond lastIndex if there are logCmds beyond lastIndex. That is fine and it does mean we
			// fill the batch opportunistically.
			var err error
			request.LogEntry, err = re.logGetEntries(nextIndex, re.node.config.logCmdBatchSize)
			if err != nil {
				return
			}
		} else {
			// We may need to send a keepalive. Check whether we are in the keepalive window.
			// We should only hit this case the first time round the loop. If we successfully
			// manage to send content, and go past lastIndex, then we bail out directly in the
			// test just after we reset nextIndex.
			if time.Since(cal.getLastAppendEntrySent()) < re.node.keepalivePeriod() {
				return
			}
		}

		//
		// Push out the message...
		ctx, cancel := context.WithTimeout(ctx, e.client.node.config.timers.gRPCTimeout)
		reply, retErr := e.client.grpcClient.AppendEntry(ctx, request)
		cancel()
		if retErr != nil {
			notifyRaftEngine(nil, retErr)
			return
		}

		cal.setLastAppendEntrySent(time.Now())

		// We need to compare term to existing term first.
		if reply.Term != currentTerm {
			notifyRaftEngine(reply, raftErrorMismatchedTerm)
			return
		}

		// We need to check for ack/nak
		if reply.Ack {
			// And move nextIndex forward
			lastAcked := prevSequence
			logCmds := len(request.LogEntry)
			if logCmds > 0 {
				lastLogCmd := request.LogEntry[logCmds-1]
				cal.nextIndex.Store(lastLogCmd.Sequence + 1)
				lastAcked = lastLogCmd.Sequence
			}

			// Update matchIndex if necessary.
			if lastAcked > cal.matchIndex.Load() {
				cal.matchIndex.Store(lastAcked)
				// If we update the matchIndex, we need to feedback to raft engine which may
				// well update the commit index.
				notifyRaftEngine(reply, nil)
			}

		} else {

			// On nak, bring nextIndex back by a batch size.
			newNext := int64(1)
			if nextIndex >= int64(re.node.config.logCmdBatchSize) {
				newNext = nextIndex - int64(re.node.config.logCmdBatchSize)
			}
			cal.nextIndex.Store(newNext)
		}

		//
		// Check... if we are past lastIndex as originally determined when we handled the event we can bail out.
		// We avoid going round the loop and hitting the earlier check which causes us to issue keepalive.
		if cal.nextIndex.Load() > lastIndex {
			return
		}

	}

}

func (e *appendEntryEvent) logKV() []interface{} {
	return append([]interface{}{"obj", "appendEntryEvent",
		"lastSent", e.cal.getLastAppendEntrySent(),
		"termOfOrigin", e.cal.termOfOrigin,
		"matchIndex", e.cal.matchIndex.Load(),
		"nextIndex", e.cal.nextIndex.Load()}, e.client.logKV()...)
}

type logCmdEvent struct {
	client    *raftClient
	container *logCommandContainer
}

func (e *logCmdEvent) handle(ctx context.Context) {

	if e.client.eventChan.discardEligibleEvent() {
		// Flush and indicate as much to the blocked call from the application.
		e.container.reply.Ack = false
		e.container.err = raftErrorf(RaftErrorLogCommandLocalDrop, "event channel to gRPC client flushed")
	} else {
		// We derive context from application context, so application controls cancellation (except if we are shutting down).
		appCtxWithCancel, cancel := context.WithCancel(e.container.appCtx)
		defer cancel()
		go func() {
			defer cancel()
			select {
			case <-ctx.Done():
			case <-appCtxWithCancel.Done():
			}
		}()
		e.container.reply, e.container.err = e.client.grpcClient.LogCommand(appCtxWithCancel, e.container.request)
	}
	// Dedicated slot in buffered channel waiting for response so this never blocks.
	e.container.returnChan <- e.container

}

func (e *logCmdEvent) logKV() []interface{} {
	return append([]interface{}{"obj", "logCmdEvent", "request", *e.container.request}, e.client.logKV()...)
}
