package raft

import (
	"context"
	"github.com/ccassar/raft/internal/raft_pb"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/atomic"
	"math/rand"
	"sync"
	"time"
)

// NodeState: describes the state of the raft node.
type nodeState string

func (state nodeState) String() string {
	switch state {
	case follower:
		return "follower"
	case candidate:
		return "candidate"
	case leader:
		return "leader"
	}
	return "illegal"
}

const (
	// Node is follower according to Raft spec. This is the initial state of the node coming up.
	follower nodeState = "follower"
	// Node is candidate according to Raft spec, during election phase.
	candidate = "candidate"
	// Node is leader in current CurrentTerm according to Raft spec.
	leader = "leader"
)

// stateFn variation of state machine; or a variation at least, as described by r@golang.orf here:
// https://talks.golang.org/2011/lex.slide
// Each state is represented as a function which has access to an input channel which brings in
// external events; namely timers or messages.
type stateFn func(context.Context) stateFn

// raftEngine is the object at the heart of the raft state machine, the spider at the centre of the web.
// Progression through the raft state machine happens from the raftEngine.run() goroutine. The messaging side
// goroutines are not intelligent and simply relieve the raftEngine from the mundane (and, more importantly,
// blocking interactions with other cluster nodes).
type raftEngine struct {
	node  *Node
	state nodeState
	// Persisted state on all nodes. We embed the protobuf derived structure for ease of serialisation.
	raft_pb.PersistedState
	// Persisted log
	logDB *bolt.DB
	// Volatile state on all nodes.
	commitIndex *atomic.Int64
	lastApplied *atomic.Int64
	// Per state content
	leaderState    *raftEngineLeader
	candidateState *raftEngineCandidate
	//
	// Channels... same channels are used irrespective of state. It is the state function which decides
	// what to do with the content in the channel. These channels allow multiple endpoints receiving messages to bring
	// those messages or responses to outbound messages to the raftEngine.
	//
	// There are in fact two groups of channels.
	// The first set bring in external requests (inbound prefix). These requests are handled synchronously.
	// The second set bring in the asynchronous responses from the gRPC client go routines.
	// IMPORTANT NOTE: Pushing to the gRPC client side must always be done with a timeout to avoid livelock.
	//
	inboundAppendEntryChan    chan *appendEntryContainer
	inboundRequestVoteChan    chan *requestVoteContainer
	inboundLogCommandChan     chan *logCommandContainer
	inboundRequestTimeoutChan chan *requestTimeoutContainer
	// The returns channels are used to return results asynchronously from the gRPC client goroutines.
	returnsAppendEntryChan    chan *appendEntryContainer
	returnsRequestVoteChan    chan *requestVoteContainer
	returnsLogCommandChan     chan *logCommandContainer
	returnsRequestTimeoutChan chan *requestTimeoutContainer
	//
	// Channel to handle log commands coming from app. These will be gRPCed to the leader which may be ourselves
	// or another node in the cluster.
	localLogCommandChan chan *logCommandContainer
	//
	// Who does the local node think is the current leader. This is set in the context of the raftEngine go routine
	// but can be accessed from an application accessor.
	currentLeader *atomic.Int32
	// publisher handles publishing of log commands which have been committed to the local application managing
	// its distributed state machine using the log commands.
	publisher *raftLogPublisher
}

// raftEngineCandidate tracks state pertinent to when a node is in candidate state. When node transitions to
// candidate state it starts with a fresh copy of the candidate structure.
type raftEngineCandidate struct {
	votesReceived map[int32]bool
}

type clientStateAtLeader struct {
	// TODO initialise to the lastLogIndex + 1
	nextIndex           int64
	matchIndex          int64
	keepaliveDue        *time.Timer
	lastAppendEntrySent time.Time
}

func (l *raftEngineLeader) validateClientIndex(clientIndex int32) bool {
	if _, ok := l.clients[clientIndex]; !ok {
		err := raftErrorf(RaftErrorOutOfBoundsClient, "failed to validate client is known to leader")
		l.engine.node.logger.Errorw("client validation failed", append(l.engine.logKV(), raftErrKeyword, err)...)
		l.engine.node.signalFatalError(err)
		return false
	} // else signal shutdown with error
	return true
}

func (l *raftEngineLeader) resetKeepalive(ctx context.Context, clientIndex int32, d time.Duration) {

	if l.validateClientIndex(clientIndex) {
		cal := l.clients[clientIndex]
		// With AfterFunc, it does not matter if Reset returns true or false. If reset raced with the expiration,
		// both winning and losing the race is fine - if we lose the race we schedule the keepalive when we could
		// have avoided it, but then we still block the keepalive on dispatch.
		cal.keepaliveDue.Reset(d)
	}
}

// raftEngineLeader tracks state pertinent to when a node is in leader state. When node transitions to
// leader state it starts with a fresh copy of the leader structure.
type raftEngineLeader struct {
	engine             *raftEngine
	clients            map[int32]*clientStateAtLeader
	clientKeepaliveDue chan int32
}

const noLeader = -1
const notVotedThisTerm = -1
const indexNotSet = -1

func (re *raftEngine) logKV() []interface{} {
	return []interface{}{
		"localNodeIndex", re.node.index,
		"currentTerm", re.CurrentTerm,
		"commitIndex", re.commitIndex.Load(),
		"appliedIndex", re.lastApplied.Load(),
		"state", re.state,
		"VotedFor", re.VotedFor,
		"currentLeader", re.currentLeader.Load()}
}

type requestVoteContainer struct {
	request    *raft_pb.RequestVoteRequest
	err        error
	reply      *raft_pb.RequestVoteReply
	returnChan chan *requestVoteContainer
}

type requestTimeoutContainer struct {
	request    *raft_pb.RequestTimeoutRequest
	err        error
	reply      *raft_pb.RequestTimeoutReply
	returnChan chan *requestTimeoutContainer
}

type appendEntryContainer struct {
	request    *raft_pb.AppendEntryRequest
	err        error
	reply      *raft_pb.AppendEntryReply
	returnChan chan *appendEntryContainer
}

type logCommandContainer struct {
	request    *raft_pb.LogCommandRequest
	err        error
	reply      *raft_pb.LogCommandReply
	returnChan chan *logCommandContainer
}

func (msg *appendEntryContainer) getIndexRangeForRequest() (int64, int64) {
	firstInRange := int64(indexNotSet)
	lastInRange := int64(indexNotSet)
	leCount := len(msg.request.LogEntry)
	if leCount > 0 {
		leFirst := msg.request.LogEntry[0]
		if leFirst != nil {
			firstInRange = leFirst.Sequence
		}
		leLast := msg.request.LogEntry[leCount-1]
		if leLast != nil {
			lastInRange = leLast.Sequence
		}
	}
	return firstInRange, lastInRange
}

func initRaftEngine(ctx context.Context, n *Node) error {

	re := &raftEngine{
		node:                      n,
		inboundAppendEntryChan:    make(chan *appendEntryContainer, n.config.channelDepth.serverEvents),
		inboundRequestVoteChan:    make(chan *requestVoteContainer, n.config.channelDepth.serverEvents),
		inboundLogCommandChan:     make(chan *logCommandContainer, n.config.channelDepth.serverEvents),
		inboundRequestTimeoutChan: make(chan *requestTimeoutContainer, n.config.channelDepth.serverEvents),
		returnsAppendEntryChan:    make(chan *appendEntryContainer, n.config.channelDepth.serverEvents),
		returnsRequestVoteChan:    make(chan *requestVoteContainer, n.config.channelDepth.serverEvents),
		returnsLogCommandChan:     make(chan *logCommandContainer, n.config.channelDepth.serverEvents),
		returnsRequestTimeoutChan: make(chan *requestTimeoutContainer, n.config.channelDepth.serverEvents),
		localLogCommandChan:       make(chan *logCommandContainer, 1),
		commitIndex:               atomic.NewInt64(0),
		lastApplied:               atomic.NewInt64(0),
		currentLeader:             atomic.NewInt32(int32(noLeader)),
	}

	n.engine = re
	err := re.initLogDB(ctx, n)
	if err != nil {
		return err
	}

	return nil
}

// raftEngine.run runs the core of the raft algorithm. We have an event loop which handles the state of the raft node,
// and which receives events; both timeouts and messages.
func (re *raftEngine) run(ctx context.Context, wg *sync.WaitGroup, n *Node) {

	n.logger.Infow("raftEngine, start running", re.logKV()...)

	defer wg.Done()

	lp := &raftLogPublisher{
		engine:           re,
		updatesAvailable: make(chan struct{}, 1),
		logCmdChannel:    re.node.config.LogCmds,
	}
	wg.Add(1)
	go lp.run(ctx, wg)

	for s := re.followerStateFn(ctx); s != nil; {
		s = s(ctx)
		n.logger.Debugw(
			"raftEngine leaving state", re.logKV()...)
	}

	n.logger.Infow("raftEngine, stop LogDB", re.logKV()...)
	re.shutdownLogDB()

	n.logger.Infow("raftEngine, stop running", re.logKV()...)
}

type termComparisonResult int

const (
	sameTerm termComparisonResult = iota
	newTerm
	staleTerm
)

func (re *raftEngine) updateVotedFor(candidate int32) {
	re.VotedFor = candidate
	re.saveNodePersistedData()
}

// replaceTrainIfNewer will test current against received term. There are three possible outcomes;
// rxed term is newer, in which case current term is updated. Otherwise, received term could be older
// or the same as current term. In all cases, return value indicates identified case.
//
// This function could result in persisted data getting updated... (i.e. new CurrentTerm, clear VotedFor).
// If persisted data needs to be updated and for some reason or another fails, we will signal failure
// and come down.
func (re *raftEngine) replaceTermIfNewer(rxTerm int64) termComparisonResult {
	switch {
	case rxTerm > re.CurrentTerm:
		// update currentTerm and votedFor - this will result in the persistent data being updated.
		re.CurrentTerm = rxTerm
		re.updateVotedFor(notVotedThisTerm)
		re.currentLeader.Store(int32(noLeader))
		re.node.logger.Debugw("raftEngine declaring new CurrentTerm", re.logKV()...)

		return newTerm
	case rxTerm < re.CurrentTerm:
		return staleTerm
	default:
		return sameTerm
	}
}

// candidateStateFn implements the behaviour of the node while in candidate state. While in candidate state
// we do not handle the localLogCommandChan - no leader to pass request too.
func (re *raftEngine) candidateStateFn(ctx context.Context) stateFn {

	defer func() {
		// Purge candidate state on the way out of this state.
		re.candidateState = nil
	}()

	re.state = candidate
	re.node.logger.Debugw("raftEngine entering state", re.logKV()...)

	var ltTimer *time.Timer

	ltTimerStop := func() {
		if !ltTimer.Stop() {
			<-ltTimer.C
		}
	}

	for {
		// Start with fresh state fro this iteration...
		re.candidateState = &raftEngineCandidate{
			votesReceived: map[int32]bool{},
		}

		for _, client := range re.node.messaging.clients {
			postMessageToClientWithFlush(ctx, client, &requestVoteEvent{
				client: client,
				container: requestVoteContainer{
					request: &raft_pb.RequestVoteRequest{
						Term:         re.CurrentTerm,
						CandidateId:  re.node.index,
						LastLogIndex: 0, // TODO
						LastLogTerm:  0, // TODO
						To:           client.index,
					},
					reply:      nil,
					err:        nil,
					returnChan: re.returnsRequestVoteChan,
				},
			})
		}

		leaderTimeout := randomiseDuration(re.node.config.timers.leaderTimeout)
		re.node.logger.Debugw("raftEngine candidate, extending leader timeout",
			append(re.logKV(), "period", leaderTimeout.String())...)

		if ltTimer == nil {
			ltTimer = time.NewTimer(leaderTimeout)
		} else {
			ltTimer.Reset(leaderTimeout)
		}

		// Time to declare a new CurrentTerm...
		re.replaceTermIfNewer(re.CurrentTerm + 1)
		re.node.logger.Debugw("raftEngine candidate, declared new term", re.logKV()...)

		// ship off request votes to clients... whatever clients were sending out in previous state, given the new CurrentTerm,
		// can be flushed too. So, what we do here, is turn on flush, post flush-off message, and send our request vote.

		// - oh and vote for myself
		re.candidateState.votesReceived[re.node.index] = true
		re.updateVotedFor(re.node.index)

	innerLoop:
		for {
			select {

			case <-ltTimer.C:
				re.node.logger.Debugw("raftEngine candidate, restart election cycle on timeout", re.logKV()...)
				break innerLoop

			case msg := <-re.inboundAppendEntryChan:

				outcome := re.handleRxedAppendEntry(msg, true)
				switch outcome {
				case senderStale:
					// continue here... waiting for outcome of election.
					re.node.logger.Debugw(
						"raftEngine candidate, ignoring AppendEntry request from stale remote client",
						append(re.logKV(), "remoteNodeIndex", msg.request.LeaderId)...)
				default: // election completed... return to follower state.
					ltTimerStop()
					re.currentLeader.Store(msg.request.LeaderId)
					re.node.logger.Debugw(
						"raftEngine candidate, AppendEntry request results in new leader for the term",
						append(re.logKV(), "remoteNodeIndex", msg.request.LeaderId)...)
					return re.followerStateFn
				}

			case msg := <-re.inboundRequestVoteChan:

				if re.handleRxedRequestVote(msg) {
					// Vote will only be granted if we moved to a new term (otherwise we'd have voted for ourselves
					// already). In this case, we will simply move back down to follower.
					ltTimerStop()
					re.node.logger.Debugw(
						"raftEngine candidate, cast vote in election",
						append(re.logKV(), "remoteNodeIndex", msg.request.CandidateId)...)
					return re.followerStateFn
				} else {
					re.node.logger.Debugw(
						"raftEngine candidate, did NOT vote on RequestVote from remote client",
						append(re.logKV(), "remoteNodeIndex", msg.request.CandidateId)...)
				}

			case msg := <-re.returnsAppendEntryChan:
				// We should have not be getting AppendEntry replies at this point. We simply discard the replies.
				re.node.logger.Debugw(
					"raftEngine candidate, ignoring AppendEntry replies from remote client",
					append(re.logKV(), "remoteNodeIndex", msg.request.To)...)

			case msg := <-re.returnsRequestVoteChan:

				if msg.err != nil {
					// an error was signalled by the sender.  This means we believe we failed to reach the specific
					// client. We simply continue. We do not retry. If necessary, we will have another election at
					// some point (i.e. when ltTimer expires if another node does not make it to leader).
					re.node.logger.Debugw(
						"raftEngine candidate, AppendEntry reply error from remote client",
						append(re.logKV(), "remoteNodeIndex", msg.request.To, raftErrKeyword, msg.err)...)
					continue
				}

				switch re.replaceTermIfNewer(msg.request.Term) {
				case staleTerm:
					re.node.logger.Debugw(
						"raftEngine candidate, ignoring RequestVote reply from stale remote client",
						append(re.logKV(), "remoteNodeIndex", msg.reply.VoterId)...)

				case sameTerm:

				case newTerm:
					re.node.logger.Debugw(
						"raftEngine candidate, received reply from client in a future term",
						append(re.logKV(), "remoteNodeIndex", msg.reply.VoterId)...)
					ltTimerStop()
					return re.followerStateFn
				}

				// Figure out whether we were granted the vote, and in particular if we have a majority.
				if msg.reply.VoteGranted && re.countVote(msg.reply.VoterId) {
					ltTimerStop()
					return re.leaderStateFn
				}

			case <-ctx.Done():
				ltTimerStop()
				re.node.logger.Debugw(
					"raftEngine candidate, received shutdown", re.logKV()...)
				return nil
			}
		}
	}

}

func (re *raftEngine) leaderStateFn(ctx context.Context) stateFn {

	re.node.logger.Debugw("raftEngine entering state", "new", leader)
	re.currentLeader.Store(re.node.index)
	re.state = leader

	defer func() {
		// Purge leader state on the way out of this state.
		re.leaderState = nil
	}()

	//
	// First thing we do when we become leaders is set up fresh state.
	re.leaderState = &raftEngineLeader{
		engine:  re,
		clients: map[int32]*clientStateAtLeader{},
	}

	// Setup per-client keepalive timers.
	// Shutdown keepalive timers when we leave this state since we will no longer want to be sending
	// keepalives when we leave leader state.
	keepalivePeriod := re.node.keepalivePeriod()
	timerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i, cal := range re.leaderState.clients {
		// Initial expiration is quasi-instant. We want to send a Keepalive right away when we expire.
		cal.keepaliveDue = time.AfterFunc(time.Microsecond, func() {
			select {
			case <-timerCtx.Done():
				return
			case re.leaderState.clientKeepaliveDue <- i:
			default:
				// if keepaliveDue channel is full, we can safely skip scheduling the keepalive. It means we
				// have not picked up enough messages, that we will be bumped from leader anyway.
			}
			re.leaderState.resetKeepalive(ctx, i, keepalivePeriod)
		})
	}

	for {
		select {

		case clientIndex := <-re.leaderState.clientKeepaliveDue:
			re.node.logger.Debugw("raftEngine leader, send keepalive",
				append(re.logKV(), "remoteNodeIndex", clientIndex)...)
			re.produceAppendEntry(ctx, clientIndex, []*raft_pb.LogEntry{})

		case msg := <-re.inboundAppendEntryChan:
			outcome := re.handleRxedAppendEntry(msg, false)
			switch outcome {
			case senderStale, rejected:
				// continue here... waiting for outcome of election.
				re.node.logger.Debugw(
					"raftEngine leader, ignoring AppendEntry request from remote client",
					append(re.logKV(), "remoteNodeIndex", msg.request.LeaderId)...)
			default: // new term, fall in.
				re.node.logger.Debugw(
					"raftEngine leader, AppendEntry request results in new term",
					append(re.logKV(), "remoteNodeIndex", msg.request.LeaderId)...)
				return re.followerStateFn
			}

		case msg := <-re.inboundRequestVoteChan:

			if re.handleRxedRequestVote(msg) {
				// Vote will only be granted if we moved to a new term (otherwise we'd have voted for ourselves
				// already). In the case we moved up a term, we will simply move back down to follower.
				re.node.logger.Debugw(
					"raftEngine leader, cast vote in election with new term",
					append(re.logKV(), "remoteNodeIndex", msg.request.CandidateId)...)
				return re.followerStateFn
			} else {
				re.node.logger.Debugw(
					"raftEngine leader, did NOT vote on RequestVote from remote client",
					append(re.logKV(), "remoteNodeIndex", msg.request.CandidateId)...)
			}

		case msg := <-re.returnsAppendEntryChan:

			if msg.err != nil {
				re.node.logger.Debugw(
					"raftEngine leader, AppendEntry reply from remote client with error",
					append(re.logKV(), "remoteNodeIndex", msg.request.To, raftErrKeyword, msg.err)...)
				continue
			}

			switch re.replaceTermIfNewer(msg.request.Term) {
			case staleTerm:
				re.node.logger.Debugw(
					"raftEngine leader, ignoring AppendEntry reply from stale remote client",
					append(re.logKV(), "remoteNodeIndex", msg.request.To)...)

			case sameTerm:

				firstInRange, lastInRange := msg.getIndexRangeForRequest()

				//
				// Exciting times. We are receiving response for AppendRequest. Result could be that we have
				// in fact committed whatever was in the log, or trigger the need to retry.
				if msg.err == nil {

					if re.leaderState.validateClientIndex(msg.request.To) {

						cal := re.leaderState.clients[msg.request.To]

						if msg.reply.Ack {
							if lastInRange != indexNotSet {
								cal.matchIndex = lastInRange
							}
							re.node.logger.Debugw(
								"raftEngine leader, received AppendEntry reply from client, ack",
								append(re.logKV(), "remoteNodeIndex", msg.request.To,
									"indexFirst", firstInRange, "indexLast", lastInRange)...)

						} else {
							// Do not update matchIndex, and this will be accounted for in the send next.
							re.node.logger.Debugw(
								"raftEngine leader, received AppendEntry reply from client, nak",
								append(re.logKV(), "remoteNodeIndex", msg.request.To,
									"indexFirst", firstInRange, "indexLast", lastInRange)...)
						}
					}

				} else {
					re.node.logger.Debugw(
						"raftEngine leader, received AppendEntry reply from client with error",
						append(re.logKV(), "remoteNodeIndex", msg.request.To,
							"indexFirst", firstInRange, "indexLast", lastInRange, raftErrKeyword, msg.err)...)
				}

			case newTerm:
				re.node.logger.Debugw(
					"raftEngine leader, received AppendEntry reply from client in a future term",
					append(re.logKV(), "remoteNodeIndex", msg.request.To)...)
				return re.followerStateFn
			}

		case msg := <-re.returnsRequestVoteChan:

			if msg.err != nil {
				// we are getting an error back, but we are leaders already so it does not matter. Presumably
				// a timeout on a vote to a client to which we have lost connectivity.
				re.node.logger.Debugw(
					"raftEngine leader, ignoring RequestVote reply from remote client with error",
					append(re.logKV(), "remoteNodeIndex", msg.request.To, raftErrKeyword, msg.err)...)
				continue
			}

			switch re.replaceTermIfNewer(msg.request.Term) {
			case staleTerm:
				re.node.logger.Debugw(
					"raftEngine leader, ignoring RequestVote reply from stale remote client",
					append(re.logKV(), "remoteNodeIndex", msg.reply.VoterId)...)

			case sameTerm:
				// this is fine. We're still receiving vote but we are already leaders so it does not matter.

			case newTerm:
				re.node.logger.Debugw(
					"raftEngine leader, received reply from client in a future term",
					append(re.logKV(), "remoteNodeIndex", msg.reply.VoterId)...)
				return re.followerStateFn
			}

		case <-ctx.Done():
			re.node.logger.Debugw(
				"raftEngine leader, received shutdown", re.logKV()...)
			return nil
		}
	}
}

// followerStateFn describes the behaviour of the node while in follower state.
func (re *raftEngine) followerStateFn(ctx context.Context) stateFn {

	re.node.logger.Debugw("raftEngine entering state", "new", follower)
	re.state = follower

	// While in follower state the following can happen...
	// - rx append entries from leader
	// - timeout leader (implicitly or in request)
	//
	var ltTimer *time.Timer
	ltTimerStop := func() {
		if !ltTimer.Stop() {
			<-ltTimer.C
		}
	}

	for {

		leaderTimeout := randomiseDuration(re.node.config.timers.leaderTimeout)
		re.node.logger.Debugw("raftEngine follower, extending leader timeout",
			append(re.logKV(), "period", leaderTimeout.String())...)

		if ltTimer == nil {
			ltTimer = time.NewTimer(leaderTimeout)
		} else {
			ltTimer.Reset(leaderTimeout)
		}

	innerLoop:
		for {
			select {

			case <-ltTimer.C:
				// Time to switch to candidate mode right here...
				ltTimer.Stop()
				re.node.logger.Debugw("raftEngine follower, leader timeout", re.logKV()...)
				return re.candidateStateFn

			case msg := <-re.inboundAppendEntryChan:

				outcome := re.handleRxedAppendEntry(msg, true)
				switch outcome {
				case senderStale:
					re.node.logger.Debugw(
						"raftEngine follower, ignoring AppendEntry request from stale remote client",
						append(re.logKV(), "remoteNodeIndex", msg.request.LeaderId)...)
				case rejected, accepted:
					// extend timeout for leader... we got some sensible message which is not stale.
					ltTimerStop()
					break innerLoop
				}

			case msg := <-re.inboundRequestVoteChan:

				if re.handleRxedRequestVote(msg) {
					// if we voted, it probably makes sense to extend our timeout
					ltTimerStop()
					re.node.logger.Debugw(
						"raftEngine follower, cast vote in election",
						append(re.logKV(), "remoteNodeIndex", msg.request.CandidateId)...)
					break innerLoop
				} else {
					re.node.logger.Debugw(
						"raftEngine follower, did NOT vote on RequestVote from remote client",
						append(re.logKV(), "remoteNodeIndex", msg.request.CandidateId)...)
				}

			case msg := <-re.returnsAppendEntryChan:
				re.node.logger.Debugw(
					"raftEngine follower, ignoring AppendEntry replies from remote client",
					append(re.logKV(), "remoteNodeIndex", msg.request.To)...)

			case msg := <-re.returnsRequestVoteChan:
				re.node.logger.Debugw(
					"raftEngine follower, ignoring RequestVote replies from remote client",
					append(re.logKV(), "remoteNodeIndex", msg.request.To)...)

			case <-ctx.Done():
				ltTimerStop()
				re.node.logger.Debugw(
					"raftEngine follower, received shutdown", re.logKV()...)
				return nil
			}
		}
	}
}

func (re *raftEngine) handleRxedRequestVote(msg *requestVoteContainer) bool {

	var grant bool
	var err error

	switch re.replaceTermIfNewer(msg.request.Term) {

	case staleTerm:
		// do not grant vote

	case sameTerm, newTerm:

		// TODO check msg.request.LastLogIndex and make sure it is at or later than this node's.
		if re.VotedFor == notVotedThisTerm {

			re.updateVotedFor(msg.request.CandidateId)
			grant = true
		}
	}

	msg.reply = &raft_pb.RequestVoteReply{
		Term:        re.CurrentTerm,
		VoterId:     re.node.index,
		VoteGranted: grant,
	}
	msg.err = err
	msg.returnChan <- msg

	return grant
}

type appendEntryRequestOutcome int

const (
	senderStale appendEntryRequestOutcome = iota
	rejected
	accepted
)

func (re *raftEngine) handleRxedAppendEntry(msg *appendEntryContainer, okInTerm bool) appendEntryRequestOutcome {
	var outcome appendEntryRequestOutcome
	var err error
	var ack bool

	checkTerm := re.replaceTermIfNewer(msg.request.Term)
	switch checkTerm {

	case staleTerm:
		// we have to fail append entry indicating our later term.
		outcome = senderStale

	case sameTerm, newTerm:

		outcome = rejected
		latestSequenceAdded := int64(indexNotSet)

		if okInTerm || checkTerm == newTerm {

			// Check we have all previous entries (prevLogIndex is in our log with the right term); accept if so,
			// reject otherwise.
			le, err := re.logEntryGet(msg.request.PrevLogIndex)
			if err == nil {
				if le != nil {
					if le.Term == msg.request.PrevLogTerm {
						//
						// Looks like we're in business. We need to handle the fact that we may have some new entries
						// to add (and possibly some to clear). Append all new entries, and purge any remaining ones if
						// we encounter a term/sequence discrepancy (as per Figure 2, Step 3 in ISUCA).
						outcome = accepted
						ack = true

						for _, newLe := range msg.request.LogEntry {
							var existingLe *raft_pb.LogEntry
							existingLe, err = re.logEntryGet(newLe.Sequence)
							if err == nil {
								if existingLe == nil {
									err = re.logEntryAdd(newLe)
								} else if existingLe.Term != newLe.Term {
									err = re.logEntriesPurgeTail(existingLe.Sequence)
									if err == nil {
										err = re.logEntryAdd(newLe)
									}
								}
							}
							if err != nil {
								break
							}

							latestSequenceAdded = newLe.Sequence
						}
					} else {
						re.node.logger.Debugw("AppendEntry rejected update, previous term mismatch", re.logKV()...)
					}
				} else {
					re.node.logger.Debugw("AppendEntry rejected update, previous index missing", re.logKV()...)
				}
			}

			if err != nil {
				re.node.signalFatalError(err)
			}
		}

		if err == nil && ack == true {
			newCommittedAndLearned := msg.request.CommittedIndex
			if latestSequenceAdded < msg.request.CommittedIndex {
				newCommittedAndLearned = latestSequenceAdded
			}
			if re.commitIndex.Load() < newCommittedAndLearned {
				// Updates only happen on this goroutine, so it is ok to simply store the new value.
				// We do need to notify the publisher that new commits are available assuming prior notification
				// is not still pending. Publisher will only check commitIndex it needs to get to after it drains
				// the notification, so no updates will ever be missed. Worst case is that sometimes, publisher
				// may get a notification and it is already up to date which is fine.
				re.commitIndex.Store(newCommittedAndLearned)
				re.node.logger.Debugw("AppendEntry handler updated commitIndex", re.logKV()...)
				select {
				case re.publisher.updatesAvailable <- struct{}{}:
				default:
				}
			}
		}

	}

	msg.reply = &raft_pb.AppendEntryReply{
		Term: re.CurrentTerm,
		Ack:  ack,
	}
	msg.err = err
	msg.returnChan <- msg

	return outcome
}

func (re *raftEngine) produceAppendEntry(ctx context.Context, clientIndex int32, le []*raft_pb.LogEntry) {

	if re.leaderState.validateClientIndex(clientIndex) {

		cal := re.leaderState.clients[clientIndex]
		if len(le) == 0 {
			// We're sending a keepalive. If we have sent an entry in the last keepalive period we can avoid sending
			// it now.
			if time.Since(cal.lastAppendEntrySent) < re.node.keepalivePeriod() {
				return
			}
		}

		client := re.node.messaging.clients[clientIndex]

		postMessageToClient(ctx, client, &appendEntryEvent{
			client: client,
			container: appendEntryContainer{
				request: &raft_pb.AppendEntryRequest{
					Term:     re.CurrentTerm,
					LeaderId: re.node.index,
					To:       clientIndex,
					// Todo - log entries
				},
				returnChan: re.inboundAppendEntryChan,
			},
		})

	} // else signal shutdown with error.
}

// countVote accumulates vote for voter along with the rest, and returns true if we have a majority.
func (re *raftEngine) countVote(voterId int32) bool {

	if re.candidateState != nil {
		re.candidateState.votesReceived[voterId] = true
		// we only ever append true values, so simply looking at size of dictionary is sufficient.
		votes := len(re.candidateState.votesReceived)
		return votes > len(re.node.config.Nodes)>>1
	}

	return false
}

func randomiseDuration(t time.Duration) time.Duration {
	return time.Duration(int64(t) + rand.Int63n(int64(t)))
}
