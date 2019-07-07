package raft

import (
	"context"
	"sync"
)

// raftLogPublisher is responsible for pushing log commands to the application via the LogCmdChannel.
// Note that none of the raft package goroutines ever block waiting on the publisher. The rate of publishing of the
// raftLogPublisher is dictated by the application consumption rate.
type raftLogPublisher struct {
	engine *raftEngine
	// A one-deep channel which indicates that commitIndex may have moved ahead of lastApplied, and that the publisher
	// may have new log commands it needs to push to the application.
	updatesAvailable chan struct{}
	// logCmdChannel is the channel used to publish log commands to the local application.
	logCmdChannel chan []byte
}

func (pub *raftLogPublisher) logKV() []interface{} {
	return []interface{}{
		"commitIndex", pub.engine.commitIndex.Load(), "lastApplied", pub.engine.lastApplied.Load(),
	}
}

// notify is called to wake up publisher to determine whether it needs to push newly committed updates to application.
func (pub *raftLogPublisher) notify() {
	select {
	case pub.updatesAvailable <- struct{}{}:
	default:
	}
}

func (pub *raftLogPublisher) run(ctx context.Context, wg *sync.WaitGroup) {

	defer wg.Done()

	re := pub.engine
	re.node.logger.Debugw("raftLogPublisher, start running", pub.logKV()...)

outerLoop:
	for {
		select {
		case <-pub.updatesAvailable:

			// Read the target. Note that between pulling the notification from the queue and reading the target,
			// the commitIndex may have changed, and a new updateAvailable notification enqueued. This means that
			// sometimes we will see spurious notifications because we would have processed the update in the
			// previous handler. This is perfectly fine. We do not add synchronisation to avoid it.
			count := 0
			target := re.commitIndex.Load()
			for index := re.lastApplied.Load() + 1; index <= target; index++ {
				// We could retrieve a batch at a time here using logGetEntries
				le, err := re.logGetEntry(index)
				if err != nil {
					re.node.signalFatalError(err)
					break outerLoop
				}
				select {
				case pub.logCmdChannel <- le.Data:
					re.lastApplied.Store(index)
				case <-ctx.Done():
					break outerLoop
				}
				count++
			}

			re.node.logger.Debugw("log command publisher", append(pub.logKV(), "published", count))

		case <-ctx.Done():
			break outerLoop
		}
	}

	re.node.logger.Debugw("raft log publisher, stop running", pub.logKV()...)
}
