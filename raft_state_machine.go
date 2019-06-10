package raft

import "context"

// NodeState: describes the state of the raft node.
type nodeState int

const (
	// Node is follower according to Raft spec. This is the initial state of the node coming up.
	follower nodeState = iota
	// Node is candidate according to Raft spec, during election phase.
	candidate
	// Node is leader in current term according to Raft spec.
	leader
)

// Generic events... note how we carry *all* the context in the event; i.e. when we produce
// an event we know all the context necessary to dispose of the event.
type event interface {
	handle(ctx context.Context)
	// Used to generate consistent k/v for logging.
	logKV() []interface{}
}

//
// This file include the core of the raft algorithm. We have an event loop which handles the state of the raft node,
// and which receives events; both timeouts and messages.
func (n *Node) mainEventLoop(ctx context.Context) {

	select {

	case <-ctx.Done():
		// This is the done signal on the inner private context; which means that by the time this is cancelled
		// we will have already transferred ownership as and if necessary (i.e. if we are leader).

	}

}
