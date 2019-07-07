package raft

import (
	"context"
	"fmt"
	"github.com/ccassar/raft/internal/raft_pb"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/atomic"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// NodeState: describes the state of the raft node.
type nodeState int

func (state nodeState) String() string {
	switch state {
	case follower:
		return "follower"
	case candidate:
		return "candidate"
	case leader:
		return "leader"
	default:
		return "uninit"
	}
}

func (state nodeState) Code() int {
	return int(state)
}

const (
	uninit nodeState = iota
	// Node is follower according to Raft spec. This is the initial state of the node coming up.
	follower
	// Node is candidate according to Raft spec, during election phase.
	candidate
	// Node is leader in current CurrentTerm according to Raft spec.
	leader
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
	// Persisted state on all nodes. We do not embed the protobuf so as to support atomic in-memory access from
	// client and raftEngine goroutines.
	votedFor    atomic.Int32
	currentTerm atomic.Int64
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
	// The returns channel for AppendEntries is used to signal data arising from client side pertinent to the
	// raftEngine side - e.g. discovery of new term, indication of newly acknowledged messages which then require
	// committedIndex updates.
	returnsAppendEntryResponsesFromClientChan chan *appendEntryResponsesFromClient
	// The returns channels are used to return results asynchronously from the gRPC client goroutines.
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
	nextIndex *atomic.Int64
	// matchIndex is set on the client goroutine as it receives results to AppendEntry requests. matchIndex
	// is read on the raftEngine side and is used to manage the committedIndex.
	matchIndex *atomic.Int64
	// termOfOrigin is immutable; set once at clientStateAtLeader creation time to reflect which term it is
	// pertinent too. termOfOrigin is accessed both on the client and raftEngine goroutines.
	termOfOrigin int64
	// Keepalive time is accessed and updated on the raftEngine thread only.
	keepaliveDue *time.Timer
	// lastAppendEntrySent is set and read on the client side to dampen unnecessary keepalives. Also displayed
	// in logKV, and so we protect it with lock.
	lastAppendEntrySentMu sync.RWMutex
	lastAppendEntrySent   time.Time
}

func (cal *clientStateAtLeader) getLastAppendEntrySent() time.Time {
	cal.lastAppendEntrySentMu.RLock()
	t := cal.lastAppendEntrySent
	cal.lastAppendEntrySentMu.RUnlock()
	return t
}

func (cal *clientStateAtLeader) setLastAppendEntrySent(t time.Time) {
	cal.lastAppendEntrySentMu.Lock()
	cal.lastAppendEntrySent = time.Now()
	cal.lastAppendEntrySentMu.Unlock()
}

func (l *raftEngineLeader) mustGetClientAtLeaderFromId(clientIndex int32) *clientStateAtLeader {
	cal, ok := l.clients[clientIndex]
	if !ok {
		err := raftErrorf(RaftErrorOutOfBoundsClient, "failed to validate client is known to leader")
		l.engine.node.logger.Errorw("client validation failed", append(l.engine.logKV(), raftErrKeyword, err)...)
		l.engine.node.signalFatalError(err)
		return nil
	} // else signal shutdown with error

	return cal
}

func (l *raftEngineLeader) resetKeepalive(clientIndex int32, d time.Duration) {

	cal := l.mustGetClientAtLeaderFromId(clientIndex)
	if cal != nil {
		// With AfterFunc, it does not matter if Reset returns true or false. If reset raced with the expiration,
		// both winning and losing the race is fine - if we lose the race we schedule the keepalive when we could
		// have avoided it, but then we still block the keepalive on dispatch.
		cal.keepaliveDue.Reset(d)
	}
}

func (l *raftEngineLeader) notifyClientToProduceAppendEntries(ctx context.Context, clientIndex int32) {
	client := l.engine.node.mustGetClientFromId(clientIndex)
	cal := l.mustGetClientAtLeaderFromId(clientIndex)
	if client != nil && cal != nil {
		client.eventChan.postMessageWithFlush(
			ctx, &appendEntryEvent{client: client, cal: cal})
		l.resetKeepalive(client.index, l.engine.node.keepalivePeriod())
	}
}

type orderedCandidates []int64

func (a orderedCandidates) Len() int           { return len(a) }
func (a orderedCandidates) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a orderedCandidates) Less(i, j int) bool { return a[i] < a[j] }

func (l *raftEngineLeader) maintainCommittedIndexForLeader() {
	oc := make(orderedCandidates, 0, len(l.clients))
	for i, cal := range l.clients {
		oc[i] = cal.matchIndex.Load()
	}

	nodeToCheck := len(l.engine.node.config.Nodes)>>1 - 1
	sort.Sort(oc)

	// committed index can be no better than the last acknoledged loge entry of the middle - 1 remote node. We know
	// more than the majority have acknowledged that value.
	l.engine.updateCommittedIndexIfNecessary(oc[nodeToCheck])

	//
	// Wake up acker if necessary. Acker will be scheduled to run and validate whether any pending acks need to be
	// issued to local terminated or remote applications.
	l.acker.notify()
}

// raftEngineLeader tracks state pertinent to when a node is in leader state. When node transitions to
// leader state it starts with a fresh copy of the leader structure.
type raftEngineLeader struct {
	engine             *raftEngine
	clients            map[int32]*clientStateAtLeader
	acker              *raftLogAcknowledger
	clientKeepaliveDue chan int32
}

const noLeader = -1
const notVotedThisTerm = -1
const indexNotSet = int64(-1)
const termNotSet = int64(0)

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

type appendEntryResponsesFromClient struct {
	request *raft_pb.AppendEntryRequest
	err     error
	reply   *raft_pb.AppendEntryReply
}

type logCommandContainer struct {
	request    *raft_pb.LogCommandRequest
	err        error
	reply      *raft_pb.LogCommandReply
	returnChan chan *logCommandContainer
	appCtx     context.Context
}

func (msg *appendEntryResponsesFromClient) getIndexRangeForRequest() (int64, int64) {
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

	n.logger.Debugw("raftEngine, initialising", n.logKV()...)

	re := &raftEngine{
		node:                      n,
		inboundAppendEntryChan:    make(chan *appendEntryContainer, n.config.channelDepth.serverEvents),
		inboundRequestVoteChan:    make(chan *requestVoteContainer, n.config.channelDepth.serverEvents),
		inboundLogCommandChan:     make(chan *logCommandContainer, n.config.channelDepth.serverEvents),
		inboundRequestTimeoutChan: make(chan *requestTimeoutContainer, n.config.channelDepth.serverEvents),
		returnsAppendEntryResponsesFromClientChan: make(chan *appendEntryResponsesFromClient, n.config.channelDepth.serverEvents),
		returnsRequestVoteChan:                    make(chan *requestVoteContainer, n.config.channelDepth.serverEvents),
		returnsLogCommandChan:                     make(chan *logCommandContainer, n.config.channelDepth.serverEvents),
		returnsRequestTimeoutChan:                 make(chan *requestTimeoutContainer, n.config.channelDepth.serverEvents),
		localLogCommandChan:                       make(chan *logCommandContainer, 1),
		commitIndex:                               atomic.NewInt64(0),
		lastApplied:                               atomic.NewInt64(0),
		currentLeader:                             atomic.NewInt32(int32(noLeader)),
		state:                                     uninit,
	}

	n.engine = re
	err := re.initLogDB(ctx, n)
	if err != nil {
		return err
	}

	return nil
}

func (re *raftEngine) logKV() []interface{} {
	return []interface{}{
		"currentTerm", re.currentTerm.Load(),
		"commitIndex", re.commitIndex.Load(),
		"appliedIndex", re.lastApplied.Load(),
		"state", re.state,
		"VotedFor", re.votedFor.Load(),
		"currentLeader", re.currentLeader.Load()}
}

func (re *raftEngine) updateState(state nodeState) {
	oldState := re.state
	re.state = state

	if re.node.metrics != nil {
		re.node.metrics.stateGauge.Set(float64(state.Code()))
	}
	re.node.logger.Infow(fmt.Sprintf("ROLE CHANGE: from %s to %s",
		strings.ToUpper(oldState.String()),
		strings.ToUpper(state.String())), re.logKV()...)
}

func (re *raftEngine) updateCurrentLeader(l int32) {
	oldLeader := re.currentLeader.Load()
	re.currentLeader.Store(l)
	if l == oldLeader {
		return
	} else if l != noLeader && oldLeader != noLeader {
		// We never expect a leader transition with one being noLeader. (We always reset to noLeader on new term).
		err := raftErrorf(RaftErrorLeaderTransitionInTerm, "leader update from %d to %d, unexpected",
			oldLeader, l)
		re.node.logger.Errorw("update current leader failure", append(re.logKV(), raftErrKeyword, err)...)
		re.node.signalFatalError(err)
		return
	}

	if re.node.metrics != nil {
		re.node.metrics.leader.Set(float64(l))
	}
	re.node.logger.Infow(fmt.Sprintf("LEADER CHANGE: from %d to %d", oldLeader, l),
		re.logKV()...)
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
			fmt.Sprintf("ROLE CHANGE: leaving %s", re.state), re.logKV()...)
	}

	n.logger.Debugw("raftEngine, stop LogDB", re.logKV()...)
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
	re.votedFor.Store(candidate)
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

	currentTerm := re.currentTerm.Load()
	switch {
	case rxTerm > currentTerm:
		// update currentTerm and votedFor - this will result in the persistent data being updated.
		re.updateVotedFor(notVotedThisTerm)
		re.updateCurrentLeader(int32(noLeader))
		re.currentTerm.Store(rxTerm)
		re.node.logger.Debugw("raftEngine declaring new CurrentTerm", re.logKV()...)

		return newTerm
	case rxTerm < currentTerm:
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

	re.updateState(candidate)

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

		lastLogTerm, lastLogIndex, err := re.logGetLastTermAndIndex()
		if err != nil {
			return nil // error logged and we will be shut down.
		}

		// Time to declare a new CurrentTerm...
		re.replaceTermIfNewer(re.currentTerm.Load() + 1)
		re.node.logger.Debugw("raftEngine candidate, declared new term", re.logKV()...)

		// ship off request votes to clients... whatever clients were sending out in previous state, given the new CurrentTerm,
		// can be flushed too. So, what we do here, is turn on flush, post flush-off message, and send our request vote.
		for _, client := range re.node.messaging.clients {
			client.eventChan.postMessageWithFlush(ctx, &requestVoteEvent{
				client: client,
				container: requestVoteContainer{
					request: &raft_pb.RequestVoteRequest{
						Term:         re.currentTerm.Load(),
						CandidateId:  re.node.index,
						LastLogIndex: lastLogIndex,
						LastLogTerm:  lastLogTerm,
						To:           client.index,
					},
					reply:      nil,
					err:        nil,
					returnChan: re.returnsRequestVoteChan,
				},
			})
		}

		// - oh and vote for myself
		re.candidateState.votesReceived[re.node.index] = true
		re.updateVotedFor(re.node.index)

		leaderTimeout := randomiseDuration(re.node.config.timers.leaderTimeout)
		re.node.logger.Debugw("raftEngine candidate, extending leader timeout",
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
					re.updateCurrentLeader(msg.request.LeaderId)
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

			case msg := <-re.returnsAppendEntryResponsesFromClientChan:
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

	re.updateCurrentLeader(re.node.index)
	re.updateState(leader)

	leaderCtx, cancel := context.WithCancel(ctx)
	var ackerWg sync.WaitGroup

	defer func(wg *sync.WaitGroup, cancel context.CancelFunc) {
		// Purge leader state on the way out of this state.
		cancel()
		re.node.logger.Debug("raftEngine waiting for acker to shutdown on the way down from leader")
		ackerWg.Wait()
		re.leaderState = nil
	}(&ackerWg, cancel)

	//
	// First thing we do when we become leaders is set up fresh state. This includes setting up the acker; independent
	// goroutine which pushes acknowledgement to local or remote applications once their log command is committed.
	// The lifetime of the acker is tied to this node being leader. When we stop being leader the defer function above
	// stops the acker, and discards its state.
	ackerWg.Add(1)
	re.leaderState = &raftEngineLeader{
		engine:             re,
		clients:            map[int32]*clientStateAtLeader{},
		clientKeepaliveDue: make(chan int32, re.node.config.channelDepth.serverEvents),
		acker:              createLogAcknowledgerAndRun(leaderCtx, &ackerWg, re.commitIndex, re.node.logger),
	}

	_, ourLastIndex, err := re.logGetLastTermAndIndex()
	if err != nil { // logged and triggered shutdown in called function on catastrophic failure.
		return nil
	}

	// Setup per-client state (e.g. keepalive ticker, indices etc.)
	// Shutdown keepalive timers when we leave this state since we will no longer want to be sending
	// keepalives when we leave leader state.

	for i := int32(0); i < int32(len(re.node.config.Nodes)); i++ {

		cal := &clientStateAtLeader{
			termOfOrigin: re.currentTerm.Load(),
			nextIndex:    atomic.NewInt64(ourLastIndex + 1),
			matchIndex:   atomic.NewInt64(0),
		}

		// Initial expiration is quasi-instant. We want to send a Keepalive right away when we expire.
		// Of course the function will be periodically rescheduled.
		cal.keepaliveDue = time.AfterFunc(
			time.Nanosecond,
			keepaliveGenerator(leaderCtx, i, re.leaderState.clientKeepaliveDue))

		re.leaderState.clients[i] = cal
	}

	for {
		select {

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
					"raftEngine leader, AppendEntry request results in new term, new leader",
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

		case msg := <-re.inboundLogCommandChan:
			re.handleLogCommandContainer(ctx, msg)

		case msg := <-re.localLogCommandChan:
			re.handleLogCommandContainer(ctx, msg)

		case clientIndex := <-re.leaderState.clientKeepaliveDue:

			client, ok := re.node.messaging.clients[clientIndex]
			if ok {
				re.node.logger.Debugw("raftEngine leader, send keepalive",
					append(re.logKV(), "remoteNodeIndex", clientIndex)...)
				client.eventChan.postMessageWithFlush(
					ctx, &appendEntryEvent{client: client, cal: re.leaderState.clients[clientIndex]})
			}
			re.leaderState.resetKeepalive(clientIndex, re.node.keepalivePeriod())

		case msg := <-re.returnsAppendEntryResponsesFromClientChan:

			// Why are we here? The client go routine handles sending the AppendEntry requests to
			// the client. It does forward up results when a) they result in update to matchIndex which
			// should cause us to reevaluate our committedIndex, b) on failure if retry is required...
			// going through the raft engine ensures retries are only rescheduled while we remain
			// leaders c) in the case it gets terms back which do not match our current term.
			//
			// TODO We need to introduce exponential backoff here.
			if msg.err != nil {
				switch errors.Cause(msg.err) {
				case raftErrorMismatchedTerm:

					switch re.replaceTermIfNewer(msg.reply.Term) {
					case staleTerm:
						re.node.logger.Debugw(
							"raftEngine leader, ignoring AppendEntry reply from stale remote client, will retry",
							append(re.logKV(), "remoteNodeIndex", msg.request.To)...)
						re.leaderState.notifyClientToProduceAppendEntries(ctx, msg.request.To)
						continue

					case newTerm:
						re.node.logger.Debugw(
							"raftEngine leader, received AppendEntry reply from client in a future term",
							append(re.logKV(), "remoteNodeIndex", msg.request.To)...)
						return re.followerStateFn
					}

				default:
					re.node.logger.Debugw(
						"raftEngine leader, error AppendEntry reply for remote client, will retry",
						append(re.logKV(), "remoteNodeIndex", msg.request.To, raftErrKeyword, msg.err)...)
					re.leaderState.notifyClientToProduceAppendEntries(ctx, msg.request.To)
				}
				continue
			}

			// The only other reason we are poked is if we need to reevaluate our committedIndex (and consequently issue
			// local or remote acks to application on the basis that client's matchIndex has been updated.)
			re.leaderState.maintainCommittedIndexForLeader()

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

// keepaliveGenerator is used as AfterFunc to schedule a keepalive for a given node.
func keepaliveGenerator(ctx context.Context, index int32, trigger chan int32) func() {
	return func() {
		select {
		case trigger <- index:
		case <-ctx.Done():
			// All timers are cancelled when leader state function exists.
		}
	}
}

// rafteEngine.handleLogCommandContainer is expected to only be called when we are leader.
func (re *raftEngine) handleLogCommandContainer(ctx context.Context, msg *logCommandContainer) {

	failed := func(msg *logCommandContainer, why string, err error) {
		re.node.logger.Debugw(
			"raftEngine leader, error handling log command from application",
			append(re.logKV(), "remoteNodeIndex", msg.request.Origin)...)
		msg.reply = &raft_pb.LogCommandReply{
			Ack:    false,
			Reason: why}
		msg.returnChan <- msg // dedicated response channel will not block.
	}

	// We have received a new inbound command. We need to start by appending it to the log.
	// Then we need to notify the gRPC clients for the remote nodes. The event passed is what
	// effectively pulls up the AppendEntries required and pushes.
	_, index, err := re.logGetLastTermAndIndex()
	if err != nil {
		failed(msg, "leader failed fetching index from persisted log, "+
			"leader resigning, please retry", err)
		return
	}
	index++
	// Now that we have an index, and know our current term, let's add it to the log.
	err = re.logAddEntry(&raft_pb.LogEntry{
		Term:     re.currentTerm.Load(),
		Sequence: index,
		Data:     msg.request.Command})
	if err != nil {
		failed(msg, "leader failure adding persisted log, "+
			"leader resigning, please retry", err)
		return
	}

	// Next we need to track the pending ack with the acker. When the index gets committed, the ack
	// will be dispatched.
	re.leaderState.acker.trackPendingAck(msg, index)
	// We need to potentially notify each client that they may need to wake up to resync.
	// To do that we send the notify event which embodies the pulling and syncing necessary.
	for _, client := range re.node.messaging.clients {
		re.leaderState.notifyClientToProduceAppendEntries(ctx, client.index)
	}
}

// followerStateFn describes the behaviour of the node while in follower state.
func (re *raftEngine) followerStateFn(ctx context.Context) stateFn {

	re.updateState(follower)

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

			case msg := <-re.returnsAppendEntryResponsesFromClientChan:
				re.node.logger.Debugw(
					"raftEngine follower, ignoring AppendEntry replies from remote client",
					append(re.logKV(), "remoteNodeIndex", msg.request.To)...)

			case msg := <-re.returnsRequestVoteChan:
				re.node.logger.Debugw(
					"raftEngine follower, ignoring RequestVote replies from remote client",
					append(re.logKV(), "remoteNodeIndex", msg.request.To)...)

			case msg := <-re.localLogCommandChan:

				leader := re.currentLeader.Load()
				var pushed bool
				if leader < int32(len(re.node.messaging.clients)) && leader >= 0 {
					client := re.node.messaging.clients[leader]
					logCmdEv := &logCmdEvent{client: client, container: msg}
					pushed = client.eventChan.postMessage(ctx, logCmdEv)
					if !pushed {
						msg.err = raftErrorf(
							RaftErrorLogCommandLocalDrop, "back pressure from gRPC client [%d]", leader)
						re.node.logger.Debugw(
							"raftEngine candidate, skipped sending logCmd to remote leader, backpressure",
							append(re.logKV(), logCmdEv.logKV()...)...)
					}
				} else {
					msg.err = raftErrorf(
						RaftErrorLogCommandLocalDrop, "no gRPC client for leader [%d]", leader)
					re.node.logger.Debugw(
						"raftEngine candidate, skipped sending logCmd to remote leader",
						append(re.logKV(), "leader", leader)...)
				}

				if !pushed {
					msg.returnChan <- msg
				}

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

		_, ourLastIndex, err := re.logGetLastTermAndIndex()
		if err == nil {
			if msg.request.LastLogIndex >= ourLastIndex {

				if re.votedFor.Load() == msg.request.CandidateId {
					// we have already granted this candidate our vote in this term. We regrant it.
					grant = true
				}
				if re.votedFor.Load() == notVotedThisTerm {
					re.updateVotedFor(msg.request.CandidateId)
					grant = true
				}
			}
		} // else we have logged error in called function already.
	}

	msg.reply = &raft_pb.RequestVoteReply{
		Term:        re.currentTerm.Load(),
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
			// reject otherwise. Note we do this even if no log entries are included. This is important because
			// rejecting the keepalive is how we sync up the client to the leader if there is a mismatch (consider the
			// case where no new log entries are added after a new election - only the keepalive is available for
			// follower to force log sync up with leader).
			firstEntry := msg.request.PrevLogIndex == 0 && msg.request.PrevLogTerm == 0
			le, err := re.logGetEntry(msg.request.PrevLogIndex)
			if err == nil {
				if firstEntry || le != nil {
					if firstEntry || le.Term == msg.request.PrevLogTerm {
						//
						// Looks like we're in business. We need to handle the fact that we may have some new entries
						// to add (and possibly some to clear). Append all new entries, and purge any remaining ones if
						// AND ONLY IF we encounter a term/sequence discrepancy (as per Figure 2, Step 3 in ISUCA).
						outcome = accepted
						ack = true

						for _, newLe := range msg.request.LogEntry {
							var existingLe *raft_pb.LogEntry
							existingLe, err = re.logGetEntry(newLe.Sequence)
							if err == nil {
								if existingLe == nil {
									err = re.logAddEntry(newLe)
								} else if existingLe.Term != newLe.Term {
									err = re.logPurgeTailEntries(existingLe.Sequence)
									if err == nil {
										err = re.logAddEntry(newLe)
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
				} else if !firstEntry {
					re.node.logger.Debugw("AppendEntry rejected update, previous index missing", re.logKV()...)
				}
			}

			// We may have discovered a new leader
			re.updateCurrentLeader(msg.request.LeaderId)
		}

		if err == nil && ack == true {
			newCommittedAndLearned := msg.request.CommittedIndex
			if latestSequenceAdded < msg.request.CommittedIndex {
				newCommittedAndLearned = latestSequenceAdded
			}
			re.updateCommittedIndexIfNecessary(newCommittedAndLearned)
		}

	}

	msg.reply = &raft_pb.AppendEntryReply{
		Term: re.currentTerm.Load(),
		Ack:  ack,
	}
	msg.err = err
	msg.returnChan <- msg

	return outcome
}

func (re *raftEngine) updateCommittedIndexIfNecessary(c int64) {
	if re.commitIndex.Load() < c {
		// Updates only happen on the raftEngine goroutine, so it is ok to simply store the new value.
		// We do need to notify the publisher that new commits are available assuming prior notification
		// is not still pending. Publisher will only check commitIndex it needs to get to after it drains
		// the notification, so no updates will never be missed. Worst case is that sometimes, publisher
		// may get a notification and it is already up to date which is fine.
		re.node.logger.Debugw("raftEngine updating commitIndex", re.logKV()...)
		re.commitIndex.Store(c)
		re.publisher.notify()
	}
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
