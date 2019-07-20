package raft

import (
	"context"
	"github.com/ccassar/raft/internal/raft_pb"
	"github.com/cenkalti/backoff"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"net"
	"sync"
	"time"
)

const defaultInactivityTriggeredPingSeconds = 1
const defaultTimeoutAfterPingSeconds = 1

// server implements the raft grpc service, server side.
type raftServer struct {
	// Cache the parent node so we can navigate up as necessary.
	node *Node
	// The TCP listener used to register the raft server.
	localListener net.Listener
	// The addr in config.Nodes which has been claimed by this node, and against which we have set up
	// a local TCP listener.
	localAddr  string
	grpcServer *grpc.Server
	// Channel to receive events and messages to drive state machine.
	eventChannel chan event
}

func (s *raftServer) AppendEntry(ctx context.Context, request *raft_pb.AppendEntryRequest) (
	*raft_pb.AppendEntryReply, error) {

	container := &appendEntryContainer{request: request, returnChan: make(chan *appendEntryContainer, 1)}

	select {
	case <-ctx.Done():
		return nil, status.Errorf(codes.Aborted, "cluster node shutting down")
	case s.node.engine.inboundAppendEntryChan <- container:
		select {
		case replyContainer := <-container.returnChan:
			return replyContainer.reply, replyContainer.err
		case <-ctx.Done():
			return nil, status.Errorf(codes.Aborted, "cluster node shutting down")
		}
	}
}

func (s *raftServer) RequestVote(ctx context.Context, request *raft_pb.RequestVoteRequest) (
	*raft_pb.RequestVoteReply, error) {
	container := &requestVoteContainer{request: request, returnChan: make(chan *requestVoteContainer, 1)}

	select {
	case <-ctx.Done():
		return nil, status.Errorf(codes.Aborted, "cluster node shutting down")
	case s.node.engine.inboundRequestVoteChan <- container:
		select {
		case replyContainer := <-container.returnChan:
			return replyContainer.reply, replyContainer.err
		case <-ctx.Done():
			return nil, status.Errorf(codes.Aborted, "cluster node shutting down")
		}
	}
}

func (s *raftServer) RequestTimeout(ctx context.Context, request *raft_pb.RequestTimeoutRequest) (
	*raft_pb.RequestTimeoutReply, error) {
	container := &requestTimeoutContainer{request: request, returnChan: make(chan *requestTimeoutContainer, 1)}

	select {
	case <-ctx.Done():
		return nil, status.Errorf(codes.Aborted, "cluster node shutting down")
	case s.node.engine.inboundRequestTimeoutChan <- container:
		select {
		case replyContainer := <-container.returnChan:
			return replyContainer.reply, replyContainer.err
		case <-ctx.Done():
			return nil, status.Errorf(codes.Aborted, "cluster node shutting down")
		}
	}
}

func (s *raftServer) LogCommand(ctx context.Context, request *raft_pb.LogCommandRequest) (*raft_pb.LogCommandReply, error) {
	container := &logCommandContainer{request: request, returnChan: make(chan *logCommandContainer, 1)}
	select {
	case <-ctx.Done():
		return nil, status.Errorf(codes.Aborted, "cluster node shutting down")
	case s.node.engine.inboundLogCommandChan <- container:
		select {
		case replyContainer := <-container.returnChan:
			return replyContainer.reply, replyContainer.err
		case <-ctx.Done():
			return nil, status.Errorf(codes.Aborted, "cluster node shutting down")
		}
	}
}

// raftServer.ApplicationLoopback is used exclusively in unit test.
func (s *raftServer) ApplicationLoopback(ctx context.Context, in *raft_pb.AppNonce) (*raft_pb.AppNonce, error) {
	return in, nil
}

func (s *raftServer) logKV() []interface{} {
	return []interface{}{"obj", "localNodeServer", "address", s.localAddr}
}

func (s *raftServer) run(ctx context.Context, wg *sync.WaitGroup, n *Node) {
	defer wg.Done()

	// cache the node we are running for.
	s.node = n

	unaryInterceptorChain := []grpc.UnaryServerInterceptor{
		grpc_ctxtags.UnaryServerInterceptor(),
	}

	if n.verboseLogging {
		unaryInterceptorChain = append(unaryInterceptorChain,
			grpc_zap.UnaryServerInterceptor(
				n.logger.Named("GRPC_S").Desugar(),
				// All results are forced to debug level
				grpc_zap.WithLevels(func(code codes.Code) zapcore.Level { return zapcore.DebugLevel })))
	}

	if n.messaging.serverUnaryInterceptorForMetrics != nil {
		unaryInterceptorChain = append(unaryInterceptorChain, n.messaging.serverUnaryInterceptorForMetrics)
	}

	// Setup the default server options, all of which can be overwritten. Default server side options
	// are aggressive and assume good connectivity between cluster nodes. These options can be overridden
	// in MakeNode configuration.
	options := []grpc.ServerOption{
		grpc.MaxConcurrentStreams(100), // aggressive max concurrent stream per transport
		grpc.KeepaliveParams(keepalive.ServerParameters{ // similarly aggressive attempt to track connection liveness
			Time:    time.Second * defaultInactivityTriggeredPingSeconds, // 10 seconds with no activity, kick client for ping
			Timeout: time.Second * defaultTimeoutAfterPingSeconds,        // no ping after the next 10 seconds, then close connection.
		}),
		// control how often a client can send a keepalive, and whether to allow keepalives with no streams.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             time.Second * 2,
			PermitWithoutStream: true,
		}),
		grpc_middleware.WithUnaryServerChain(unaryInterceptorChain...),
	}

	//
	// Append configured options so they can overwrite the defaults too.
	if n.config.serverOptionsFn != nil {
		options = append(options, n.config.serverOptionsFn(s.localAddr)...)
	}

	s.grpcServer = grpc.NewServer(options...)
	reflection.Register(s.grpcServer)
	raft_pb.RegisterRaftServiceServer(s.grpcServer, s)

	n.logger.Debugw("gRPCServer starting up", s.logKV()...)

	go func() {
		select {
		case <-ctx.Done():
			n.logger.Debugw("gRPCServer graceful shut down requested", s.logKV()...)
			s.grpcServer.GracefulStop()
		}
	}()

	err := backoff.RetryNotify(
		func() error {
			return s.grpcServer.Serve(s.localListener)
		},
		backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 0),
		func(err error, next time.Duration) {
			err = raftErrorf(err, "gRPC server stopped serving, will retry")
			n.logger.Errorw(
				"gRPC Server exit", append(s.logKV(), raftErrKeyword, err, "retryIn", next.String())...)
		})
	if err != nil {
		n.logger.Errorw("gRPCServer shut down unexpectedly", append(s.logKV(), raftErrKeyword, err)...)
	} else {
		n.logger.Debugw("gRPCServer shut down gracefully", s.logKV()...)
	}
}

// Given a configuration, we need to identify which address/protocol we can use. We are careful to iterate through
// all instances and test setting up receiver (i.e. raft server) on subset with local endpoint, and finally hang
// on to the one which is free.
func initServer(ctx context.Context, n *Node) error {

	if n.index < int32(len(n.config.Nodes)) {

		le := n.config.Nodes[n.index]
		var listener net.Listener

		err := backoff.Retry(
			func() error {
				var err error
				listener, err = net.Listen("tcp", le)
				return err
			},
			backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 3),
		)

		// listener, err := net.Listen("tcp", le)
		if err != nil {
			err = raftErrorf(err, "failed to acquire local TCP socket for gRPC")
			n.logger.Errorw("initServer failed (some other application or previous instance still using socket?)",
				raftErrKeyword, err)
			return err
		}

		s := &raftServer{
			node:          n,
			localListener: listener,
			localAddr:     le,
		}

		n.messaging.server = s
		n.logger.Debugw("listener acquired local node address", s.logKV()...)

	} else {

		err := raftErrorf(
			RaftErrorServerNotSetup, "LocalNodeIndex in out of bounds of cluster Nodes configuration")
		n.logger.Errorw("initServer failed", raftErrKeyword, err)
		return err

	}

	return nil
}

// The raft client is very mechanical and simple. Its role is simply to offload the blocking gRPC calls. In some cases
// events posted by the core raft component over the channel is acknowledged with a return event. In other cases,
// the event handler is more sophisticated - e.g. AppendEntry events are just wake-up notifications which then
// result in the event handler, in the context of the client go routine, pulling and producing updates on demand.
//
type raftClient struct {
	// Cache the parent node so we can navigate up as necessary.
	node *Node
	// Immutable index of the cluster, with local significance. (If the same configuration is used
	// across nodes, then the index will match across nodes but it does NOT have to.)
	index int32
	// RemoteAddress is set at creation and immutable from there on.
	remoteAddress string
	// grpcClient tracks the grpcClient, and is only accessed in the run goroutine for the client.
	grpcClient raft_pb.RaftServiceClient
	// flushable event channel receives all events that need disposing of. Event will carry all the context
	// required to communicate with remote node and handle response. The client is as thin as can be and
	// a simple way to enable async handling of events.
	eventChan flushableEventChannel
}

func (c *raftClient) logKV() []interface{} {
	return []interface{}{"obj", "remoteNodeClient", "remoteNodeIndex", c.index, "address", c.remoteAddress}
}

// raftClient.run is a per remote node goroutine which will maintain a gRPC client connection to the remote node,
// posts to the main state machine.
func (c *raftClient) run(ctx context.Context, wg *sync.WaitGroup, n *Node) {
	defer wg.Done()

	n.logger.Debugw("remote node client worker start running", c.logKV()...)

	// Add grpc client interceptor for logging, and metrics collection (if enabled). We do not use payload logging
	// because it is currently nailed to InfoLevel.
	gcl := n.logger.Named("GRPC_C").Desugar()
	unaryInterceptorChain := []grpc.UnaryClientInterceptor{}
	if c.node.verboseLogging {
		unaryInterceptorChain = append(unaryInterceptorChain,
			grpc_zap.UnaryClientInterceptor(
				gcl, grpc_zap.WithLevels(func(code codes.Code) zapcore.Level { return zapcore.DebugLevel })))
	}

	if n.messaging.clientUnaryInterceptorForMetrics != nil {
		unaryInterceptorChain = append(unaryInterceptorChain, n.messaging.clientUnaryInterceptorForMetrics)
	}

	// Prepend our options such that they can be overridden by the client options if they overlap.
	options := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    time.Second * defaultInactivityTriggeredPingSeconds,
			Timeout: time.Second * defaultTimeoutAfterPingSeconds,
		}),
		grpc.WithUnaryInterceptor(grpc_middleware.ChainUnaryClient(unaryInterceptorChain...))}

	// Append client provided dial options specifically for this client to server connection.
	if n.config.clientDialOptionsFn != nil {
		options = append(options, n.config.clientDialOptionsFn(n.messaging.server.localAddr, c.remoteAddress)...)
	}

	conn, err := grpc.DialContext(ctx, c.remoteAddress, options...)
	if err != nil {
		if ctx.Err() == nil {
			// This is not a shutdown. We have taken a fatal error (i.e. this is not a transient error). Possibly
			// a misconfiguration of the options, for example. We will return a fatal error.
			n.logger.Errorw("remote node client worker aborting", append(c.logKV(), raftErrKeyword, err)...)
			n.signalFatalError(raftErrorf(
				RaftErrorClientConnectionUnrecoverable, "grpc client connection to remote node, err [%v]", err))
		}
		return
	}

	defer func() { _ = conn.Close() }()

	n.logger.Debugw("remote node client worker connected",
		append(c.logKV(), "connState", conn.GetState().String())...)
	c.grpcClient = raft_pb.NewRaftServiceClient(conn)

	for {
		select {
		case e := <-c.eventChan.channel:
			// The event handler carries all the context necessary, and equally handles the
			// feedback based on the outcome of the event.
			e.handle(ctx)

		case <-ctx.Done():
			// We're done. By this point we will have cleaned up and we're ready to go.
			n.logger.Debugw("remote node client worker shutting down", c.logKV()...)
			return
		}

	}

}

func initClients(ctx context.Context, n *Node) error {

	// Expectation at this point is that the server (local endpoint) has been identified, and listener is enabled on
	// it.
	if n.messaging == nil || n.messaging.server.localAddr == "" {
		err := raftErrorf(RaftErrorServerNotSetup, "failed to set up clients, local endpoint not identified yet")
		n.logger.Errorw("server should be set up successfully prior to client setup", raftErrKeyword, err)
		return err
	}

	// In this function, we set up a structure and channel for the remote nodes. We only set up the state - we do
	// not fire up the goroutines for each node yet.
	clients := map[int32]*raftClient{}
	for i, remoteNodeAddress := range n.config.Nodes {
		if remoteNodeAddress != n.messaging.server.localAddr {
			client := &raftClient{
				node:          n,
				index:         int32(i),
				remoteAddress: remoteNodeAddress,
				// Event channels is how the core of raft communicates with a gRPC client to the remote node
				// associated with this client. The event itself carries all the handling context.
				eventChan: NewFlushableEventChannel(n.config.channelDepth.clientEvents),
			}
			clients[int32(i)] = client
			n.logger.Debugw("added remote node from configuration", client.logKV()...)

		}
	}
	n.messaging.clients = clients

	return nil
}

type raftMessaging struct {
	server  *raftServer
	clients map[int32]*raftClient
	//
	// Metrics interceptors...
	clientUnaryInterceptorForMetrics func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error
	serverUnaryInterceptorForMetrics func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error)
}

// initMessaging sets up both client workers used to push messages to cluster nodes, and server side handling of
// messages sent to the local instance.
func initMessaging(ctx context.Context, n *Node) error {

	n.logger.Debugw("raftMessaging, initialising", n.logKV()...)

	if n.verboseLogging {
		// Not quite from init functions because we let user control it, but early on enough.
		grpc_zap.ReplaceGrpcLogger(n.logger.Desugar().Named("grpc"))
	}

	if n.metrics != nil {
		// Setup of grpc metrics depends on a) whether application is exporting metrics, and on top of that,
		// whether it is using the default registry or not - prom library require different setup.
		// Eventually it might be a good idea to allow application to customise counter/histogram opts too
		// by passing them in as part of WithMetrics option at configuration time.
		if n.metrics.registry != prometheus.DefaultRegisterer {

			cm := grpc_prometheus.NewClientMetrics()
			if n.metrics.detailed {
				cm.EnableClientHandlingTimeHistogram()
			}
			n.metrics.registry.MustRegister(cm)
			n.messaging.clientUnaryInterceptorForMetrics = cm.UnaryClientInterceptor()

			sm := grpc_prometheus.NewServerMetrics()
			if n.metrics.detailed {
				sm.EnableHandlingTimeHistogram()
			}
			n.metrics.registry.MustRegister(sm)
			n.messaging.serverUnaryInterceptorForMetrics = sm.UnaryServerInterceptor()

		} else if n.metrics.detailed {
			grpc_prometheus.EnableHandlingTimeHistogram()
			grpc_prometheus.EnableClientHandlingTimeHistogram()
			n.messaging.clientUnaryInterceptorForMetrics = grpc_prometheus.UnaryClientInterceptor
			n.messaging.serverUnaryInterceptorForMetrics = grpc_prometheus.UnaryServerInterceptor
		}

	}

	err := initServer(ctx, n)
	if err != nil {
		return err
	}

	err = initClients(ctx, n)
	if err != nil {
		return err
	}

	return nil
}

/*
 * runMessaging
 * Run the raft server and client side - i.e. the server which handles the grpc RPCs server side and the client side
// threads which serialise messages to a given cluster node.
 * The server side terminates RPC calls and serialises them to the main state machine event loop.
*/
func runMessaging(ctx context.Context, wg *sync.WaitGroup, n *Node) {

	defer wg.Done()

	for _, client := range n.messaging.clients {
		wg.Add(1)
		go client.run(ctx, wg, n)
	}

	wg.Add(1)
	go n.messaging.server.run(ctx, wg, n)

}
