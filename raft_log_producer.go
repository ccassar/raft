package raft

import (
	"context"
	"github.com/ccassar/raft/internal/raft_pb"
)

// raft_log_producer handles receiving log commands from the client and publishing them to the cluster
// local or remote.

// node.LogProduce is a blocking call which accepts a log command request from the application,
// and returns an error if log command failed to commit. The implementation takes care of proxying the request
// and finding and forwarding the request to the current leader.
//
// LogProduce can carry a batch of commands as data. These are treated atomically by raft. This is a slightly cheeky
// and effortless way of improving throughput through the system.
func (n *Node) LogProduce(ctx context.Context, data []byte) error {

	// Prep return channel for result.
	returnChan := make(chan *logCommandContainer, 1)

	container := &logCommandContainer{
		request:    &raft_pb.LogCommandRequest{},
		returnChan: returnChan,
		appCtx:     ctx,
	}

	select {
	case n.engine.localLogCommandChan <- container:
	case <-ctx.Done():
		return raftErrorf(ctx.Err(), "log command operation aborted")
	}

	select {
	case result := <-returnChan:

		if result.err != nil {
			return result.err
		}

		if !result.reply.Ack {
			return raftErrorf(RaftErrorLogCommandRejected, "log command operation rejected")
		}

		return nil

	case <-ctx.Done():
		return raftErrorf(ctx.Err(), "log command operation aborted")
	}

}
