package raft

import (
	"context"
	"errors"
	"github.com/ccassar/raft/raft_pb"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/grpc-ecosystem/go-grpc-middleware/logging/zap"
	grpc_ctxtags "github.com/grpc-ecosystem/go-grpc-middleware/tags"
	"github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"net"
	"strings"
	"sync"
	"time"
)

const defaultInactivityTriggeredPingSeconds = 10
const defaultTimeoutAfterPingSeconds = 10

// server implements the raft grpc service, server side.
type raftServer struct {
	// Cache the parent node so we can navigate up as necessary.
	node *Node
	// Immutable index of the local node in the cluster config, with local significance.
	// (If the same configuration is used across nodes, then the index will match across nodes but
	// it does NOT have to.)
	index int
	// The TCP listener used to register the raft server.
	localListener net.Listener
	// The addr in config.Nodes which has been claimed by this node, and against which we have set up
	// a local TCP listener.
	localAddr  string
	grpcServer *grpc.Server
	// Channel to receive events and messages to drive state machine.
	eventChannel chan event
}

func (s *raftServer) AppendEntry(context.Context, *raft_pb.AppendEntryRequest) (*raft_pb.AppendEntryReply, error) {
	return nil, status.Error(codes.Unimplemented, "AppendEntry not implemented yet")
}

func (s *raftServer) RequestVote(context.Context, *raft_pb.RequestVoteRequest) (*raft_pb.RequestVoteReply, error) {
	return nil, status.Error(codes.Unimplemented, "RequestVote not implemented yet")
}

func (s *raftServer) TimeoutRequest(context.Context, *raft_pb.TimeoutNowRequest) (*raft_pb.TimeoutNowReply, error) {
	return nil, status.Error(codes.Unimplemented, "TimeoutRequest not implemented yet")
}

func (s *raftServer) logKV() []interface{} {
	return []interface{}{"obj", "localNode", "nodeIndex", s.index, "address", s.localAddr}
}

func (s *raftServer) run(ctx context.Context, wg *sync.WaitGroup, n *Node) {
	defer wg.Done()

	// cache the node we are running for.
	s.node = n

	unaryInterceptorChain := []grpc.UnaryServerInterceptor{
		grpc_ctxtags.UnaryServerInterceptor(),
		grpc_zap.UnaryServerInterceptor(
			n.logger.Desugar(),
			grpc_zap.WithDecider(func(fullMethodName string, err error) bool {
				// will not log gRPC calls if it was a call to healthcheck and no error was raised
				if err == nil && fullMethodName == "" {
					return false
				}

				// by default everything will be logged
				return true
			})),
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
	if n.config.ServerOptionsFn != nil {
		options = append(options, n.config.ServerOptionsFn(s.localAddr)...)
	}

	s.grpcServer = grpc.NewServer(options...)
	reflection.Register(s.grpcServer)
	raft_pb.RegisterRaftServiceServer(s.grpcServer, s)

	n.logger.Infow("gRPCServer starting up", s.logKV()...)

	// Wait for shutdown and graceful exit
	go func() {
		select {
		case <-ctx.Done():
			n.logger.Infow("gRPCServer graceful shut down requested", s.logKV()...)
			s.grpcServer.GracefulStop()
		}
	}()

	for {
		err := s.grpcServer.Serve(s.localListener)
		if err != nil {
			// We should not receive an error if we exited gracefully; i.e. context cancellation. If we exit in error,
			// we restart the server, after a short wait (backoff may be useful here).
			err := raftErrorf(err, "gRPC server stopped serving unexpectedly")
			n.logger.Errorw("gRPC Server exit", append(s.logKV(), raftErrKeyword, err)...)
			// TODO Backoff...
		} else {
			// We're done...
			break
		}
	}
	n.logger.Infow("gRPCServer shut down gracefully", s.logKV()...)
}

// Given a configuration, we need to identify which address/protocol we can use. We are careful to iterate through
// all instances and test setting up receiver (i.e. raft server) on subset with local endpoint, and finally hang
// on to the one which is free.
func initServer(ctx context.Context, n *Node) error {

	errs := []string{}

	for i, le := range n.config.Nodes {
		listener, err := net.Listen("tcp", le)
		if err == nil {

			s := &raftServer{
				index:         i,
				localListener: listener,
				localAddr:     le,
			}

			n.messaging.server = s
			n.logger.Infow("listener acquired local node address", s.logKV()...)
			return nil
		}
		// we expect to fail for all but one. We do not bother to be selective about which ones we try and
		// simply rely on the failures trying to open socket for listening. Unless we fail to open any.
		errs = append(errs, err.Error())
	}

	err := raftErrorf(errors.New(strings.Join(errs, ", ")), "failed to set up local TCP socket for gRPC")
	n.logger.Errorw("listener failed to acquire node address", raftErrKeyword, err)

	return err
}

// The raft client is very mechanical and simple. Its role is simply to offload the blocking gRPC calls. Every
// event posted by the core raft component over the eventChannel is acknowledged with a return event - no ifs,
// no buts... always.
//
// Note: if client is for a leader, it will be sent an AppendEntryRequest with no log message, every heartbeat
// period if the appendEntryChannel is empty; otherwise we consider an AppendEntryRequest is scheduled already
// so there is no need for an empty one to be sent.
type raftClient struct {
	// Immutable index of the cluster, with local significance. (If the same configuration is used
	// across nodes, then the index will match across nodes but it does NOT have to.)
	index int
	// RemoteAddress is set at creation and immutable from there on.
	remoteAddress string
	// grpcClient tracks the grpcClient, and is only accessed in the run goroutine for the client.
	grpcClient raft_pb.RaftServiceClient
	// eventChannel is consumed in the run goroutine of the client, and produced too from the core
	// raft.
	eventChannel chan event
}

func (c *raftClient) logKV() []interface{} {
	return []interface{}{"obj", "remoteNode", "nodeIndex", c.index, "address", c.remoteAddress}
}

// raftClient.run is a per remote node goroutine which will maintain a gRPC client connection to the remote node,
// posts to the main state machine.
func (c *raftClient) run(ctx context.Context, wg *sync.WaitGroup, n *Node) {
	defer wg.Done()

	n.logger.Infow("remote node client worker start running", c.logKV()...)

	// Add grpc client interceptor for logging, and metrics collection (if enabled).

	unaryInterceptorChain := []grpc.UnaryClientInterceptor{
		grpc_zap.PayloadUnaryClientInterceptor(
			n.logger.Desugar(), func(ctx context.Context, fullMethodName string) bool {
				//
				// Only log RequestVote messages
				if fullMethodName == "/raft.RaftService/RequestVote" {
					return true
				}
				return false
			})}

	if n.messaging.clientUnaryInterceptorForMetrics != nil {
		unaryInterceptorChain = append(unaryInterceptorChain, n.messaging.clientUnaryInterceptorForMetrics)
	}

	// Prepend our options such that they can be overridden by the client options if they overlap.
	options := []grpc.DialOption{
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:    time.Second * defaultInactivityTriggeredPingSeconds,
			Timeout: time.Second * defaultTimeoutAfterPingSeconds,
		}),
		grpc.WithUnaryInterceptor(grpc_middleware.ChainUnaryClient(unaryInterceptorChain...))}

	// Append client provided dial options specifically for this client to server connection.
	if n.config.ClientDialOptionsFn != nil {
		options = append(options, n.config.ClientDialOptionsFn(n.messaging.server.localAddr, c.remoteAddress)...)
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

	defer conn.Close()

	n.logger.Infow("remote node client worker connected",
		append(c.logKV(), "connState", conn.GetState().String())...)
	c.grpcClient = raft_pb.NewRaftServiceClient(conn)

	for {
		select {

		case e := <-c.eventChannel:
			// The event handler carries all the context necessary, and equally handles the
			// feedback based on the outcome of the event. (The handler must also manage counting).
			// (Logging is a bit expensive, but nice and consistent!)
			n.logger.Debugw("client, dispatch event", append(c.logKV(), e.logKV()...)...)
			e.handle(ctx)

		case <-ctx.Done():
			// We're done. By this point we will have cleaned up and we're ready to go.
			n.logger.Infow("remote node client worker shutting down", c.logKV()...)
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
	clients := []*raftClient{}
	for i, remoteNodeAddress := range n.config.Nodes {
		if remoteNodeAddress != n.messaging.server.localAddr {
			client := &raftClient{
				index:         i,
				remoteAddress: remoteNodeAddress,
				// Event channel is how the core of the raft communicates with a gRPC client to the remote node
				// associated with this client.
				eventChannel: make(chan event, n.config.ChannelDepth.ClientEvents),
			}
			clients = append(clients, client)
			n.logger.Infow("added remote node from configuration", client.logKV()...)

		}
	}
	n.messaging.clients = clients

	return nil
}

type raftMessaging struct {
	server  *raftServer
	clients []*raftClient
	//
	// Metrics interceptors...
	clientUnaryInterceptorForMetrics func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error
	serverUnaryInterceptorForMetrics func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error)
	// grpcLogger indicates whether client has requested grpc logging to be enabled or not.
	grpcLogging bool
}

// initMessaging sets up both client workers used to push messages to cluster nodes, and server side handling of
// messages sent to the local instance.
func initMessaging(ctx context.Context, n *Node) error {

	if n.messaging.grpcLogging {
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
