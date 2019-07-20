package raft

import (
	"context"
	"github.com/ccassar/raft/internal/raft_pb"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"sync"
)

// raftLogAcknowledger is responsible for generating acknowledgements for log commands to the application.
// Note that none of the raft package goroutines ever block waiting on ack generation.

// We track the logCommandContainer which describes how to ack back to the application (whether local or remote),
// and the index which, once committed, triggers the acknowledgment.
type logCommandPendingAck struct {
	index     int64
	container *logCommandContainer
}

type raftLogAcknowledger struct {
	target *atomic.Int64
	// A one-deep channel which indicates that commitIndex may have moved ahead of lastApplied, and that the publisher
	// may have new log commands it needs to push to the application.
	updatesAvailable chan struct{}
	// pendingAcks tracks pending acknowledgements. This slice is added to by the raftEngine when it receives
	// new log commands through trackPEndingAck call, and is drained from the front by this ack generator. No
	// need for read/write mutex since we only have one reader and one writer contending for the resource.
	pendingAcksMu sync.Mutex
	pendingAcks   []*logCommandPendingAck
}

// trackPendingAck is called to track pending ack for a given sequence. This call must be made before the
// index is committed.
func (acker *raftLogAcknowledger) trackPendingAck(cmd *logCommandContainer, index int64) {
	acker.pendingAcksMu.Lock()
	defer acker.pendingAcksMu.Unlock()
	acker.pendingAcks = append(acker.pendingAcks, &logCommandPendingAck{index: index, container: cmd})
}

// notify is called to wake up acknowledger to determine whether it needs to acknowledge newly committed updates
// to application (local or remote).
func (acker *raftLogAcknowledger) notify() {
	select {
	case acker.updatesAvailable <- struct{}{}:
	default:
	}
}

func (acker *raftLogAcknowledger) run(ctx context.Context, wg *sync.WaitGroup, lg *zap.SugaredLogger) {

	defer wg.Done()

	lg.Debug("raftLogAcknowledger, start running")

outerLoop:
	for {
		select {
		case <-acker.updatesAvailable:

			target := acker.target.Load()
			//
			// Find any acknowledgments pending beneath the target and release them.
			var pendingFrom, pendingTo int64
			count := 0
			pending := 0
			for {
				var doAck *logCommandPendingAck
				acker.pendingAcksMu.Lock()
				if len(acker.pendingAcks) > 0 {
					doAck = acker.pendingAcks[0]
					if doAck.index <= target {
						acker.pendingAcks = acker.pendingAcks[1:]
					} else {
						doAck = nil
						pending = len(acker.pendingAcks)
						if pending > 0 {
							pendingFrom = acker.pendingAcks[0].index
							pendingTo = acker.pendingAcks[pending-1].index
						}
					}
				}
				acker.pendingAcksMu.Unlock()

				if doAck == nil {
					// wait for next notification
					break
				}

				count++
				// Acknowledge and return the container.
				doAck.container.reply = &raft_pb.LogCommandReply{
					Ack: true,
				}
				// Channel of one for reply is always available, so no need to mess about.
				doAck.container.returnChan <- doAck.container
			}
			if count > 0 {
				lg.Debugw(
					"raftLogAcknowledger, sent acknowledgements to applications",
					"count", count, "target", target,
					"pendingCount", pending, "pendingFrom", pendingFrom, "pendingTo", pendingTo)
			}

		case <-ctx.Done():

			// We are shutting down or demoted from leader. Nak any remaining pending entries, and leave.
			lg.Debug("raftLogAcknowledger, received shutdown (or resigning from leader)")

			acker.pendingAcksMu.Lock()
			var pendingFrom, pendingTo int64
			pending := len(acker.pendingAcks)
			if pending > 0 {
				pendingFrom = acker.pendingAcks[0].index
				pendingTo = acker.pendingAcks[pending-1].index
			}

			for _, doAck := range acker.pendingAcks {
				doAck.container.err = ctx.Err()
				// Channel of one for reply is always available, so no need to mess about.
				doAck.container.returnChan <- doAck.container
			}
			acker.pendingAcksMu.Unlock()

			if pending > 0 {
				lg.Debugw(
					"raftLogAcknowledger, sent negative acknowledgements to applications for pending acks",
					"pendingCount", pending, "pendingFrom", pendingFrom, "pendingTo", pendingTo)
			}

			break outerLoop
		}
	}

	lg.Debug("raftLogAcknowledger, stop running")
}

// createLogAcknowledgerAndRun is typically called by the leader to set up its acknowledger.
func createLogAcknowledgerAndRun(
	ctx context.Context, wg *sync.WaitGroup, target *atomic.Int64, lg *zap.SugaredLogger) *raftLogAcknowledger {

	defer wg.Done()

	acker := &raftLogAcknowledger{
		target:           target,
		updatesAvailable: make(chan struct{}, 1),
		pendingAcks:      []*logCommandPendingAck{},
	}

	wg.Add(1)
	go acker.run(ctx, wg, lg)

	return acker
}
