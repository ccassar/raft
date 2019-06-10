package raft

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"sync"
)

// NodeConfig is, well,  configuration for the local node. Package expects configuration to be passed in when starting
// up the node using MakeNode.
type NodeConfig struct {
	// Raft cluster node addresses. Nodes should include all the node addresses including the local one, in the
	// form address:port. This makes configuration easy in that all nodes can share the same configuration. Each
	// node will attempt to find a local address/port which works and registers to communicate over gRPC with
	// both clients and other nodes on those ports if they work. Note that this works even when running multiple
	// nodes on the same host - each node simply races to listen on a socket.
	//
	// The order of the nodes (ignoring the local node) is also interpreted as the order of preference to transfer
	// leadership to in case we need to transfer. Note that this tie breaker only kicks in amongst nodes
	// which have a log matched to ours.
	Nodes []string
	//
	// Pass in method which provides dial options to use when connecting as gRPC client with other nodes as servers.
	// Exposing this configuration allows application to determine whether, for example, to use TLS in raft exchanges.
	// The callback passes in the local node and remote node (as in Nodes above) for which we are setting up client
	// connection.
	ClientDialOptionsFn func(local, remote string) []grpc.DialOption
	// Pass in method which provides server side grpc options. These will be merged in with default options, with
	// default options overridden if provided in configuration. The callback passes in the node picked as local
	// for this node. This is usually obvious except in cases (typically test) where multiple of the addresses in
	// Nodes are local (in which case the first free available socket is used.
	ServerOptionsFn func(local string) []grpc.ServerOption
	//
	// Channel depths, if not set will default to sensible values.
	ChannelDepth struct {
		ServerEvents int32
		ClientEvents int32
	}
}

// NewNodeConfig returns a NodeConfig structure initialised with sensible defaults where possible. Caller,
// will need to setup Nodes as a minimum before using NodeConfig in MakeNode.
func NewNodeConfig() NodeConfig {

	nc := NodeConfig{}

	return nc
}

const mIN_NODES_IN_CLUSTER = 3

// NodeConfig.validate: provides validation function for the configuration presented by user. Defaults are also
// set if necessary.
func (cfg *NodeConfig) validate() error {

	if len(cfg.Nodes) < mIN_NODES_IN_CLUSTER {
		return raftErrorf(
			RaftErrorMissingNodeConfig,
			"not enough endpoints specified in Nodes %s, expect at least %d "+
				"e.g. 'n1.example.com:443','n3.example.com:443','n3.example.com:443'",
			cfg.Nodes, mIN_NODES_IN_CLUSTER)
	}

	if cfg.ClientDialOptionsFn == nil {
		return raftErrorf(
			RaftErrorMissingNodeConfig,
			"no dial options method is provided in ClientDialOptionsFn, either TLS or grpc.WithInsecure() "+
				"option must be provided. Raft does NOT default to insecure unless explicitly requested by application",
			mIN_NODES_IN_CLUSTER)
	}

	if cfg.ChannelDepth.ClientEvents == 0 {
		cfg.ChannelDepth.ClientEvents = 32
	}

	if cfg.ChannelDepth.ServerEvents == 0 {
		cfg.ChannelDepth.ServerEvents = 32
	}

	return nil
}

// followerNode objects are used by leader to track remote node state.
type followerNode struct {
}

// This node will own a LeaderRole structure if it is a leader.
type leaderRole struct {
	followers []*followerNode
	// resignToFirstMatchingFollower is set to true when we decide to transfer leadership.
	// We have decided to resign. We implement the leadership transfer extension, and when a followed matches us,
	// we resign by sending a TimeoutNow message.
	resignToFirstMatchingFollower bool
}

// This node will own a followerRole structure if it is a follower.
type followerRole struct {
}

// This node will own a candidateRole structure if it is a candidate.
type candidateRole struct {
}

// Node tracks the state and configuration of this local node. Public access to services provided by node are concurrency
// safe. Node structure carries the state of the local running raft instance.
type Node struct {
	// Readonly state provided when the Node is created.
	config *NodeConfig
	// Server and client side state for messaging (independent of role). Messaging is message largely in raft_grpc.go.
	messaging *raftMessaging
	// LeaderRole structure maintains leader role context when local node is a leader.
	state nodeState
	// fatalErrorFeedback feeds back fatal errors to the client.
	// Do not push into channel directly; use signalFatalError().
	fatalErrorFeedback chan error
	// We also remember we have take a fatal error, in order to avoid graceful shutdown attempt.
	fatalErrorCount *atomic.Int32
	// Track rootCancel function used to clean up autonomously on fatal errors.
	cancel context.CancelFunc
	// metrics structure associated with this node.
	metrics *metricsHolder
	// logger for Node, configurable through WithLogger, or WithPreConfiguredLogger options.
	logger *zap.SugaredLogger
}

// FatalErrorChannel returns an error channel which is used by the raft Node to signal an unrecoverable failure
// asynchronously to the application. Such errors are expected to occur with vanishingly small probability.
// An example of such an error would be if the dial options or gRPC server options provided make it impossible
// for the client to successfully connect with the server (RaftErrorClientConnectionUnrecoverable). When a fatal
// error is registered, raft package will stop operating, and will mark the root wait group done.
func (n *Node) FatalErrorChannel() chan error {
	return n.fatalErrorFeedback
}

func (n *Node) logKV() []interface{} {

	kv := []interface{}{"obj", "Node"}

	if n.messaging.server != nil {
		kv = append(kv, n.messaging.server.logKV()...)
	}

	kv = append(kv, "clients", len(n.messaging.clients), "fatalErrorCount", n.fatalErrorCount.Load())

	return kv
}

// signalFatalError allows package to indicate fatal error to user. This will typically be followed by the client
// shutting down by cancelling context. If the buffered channel is full, we would just skip asking yet again.
func (n *Node) signalFatalError(err error) {

	n.fatalErrorCount.Inc()

	select {
	case n.fatalErrorFeedback <- err:
		n.logger.Errorw("raft, signalling fatal error", rAFT_ERR_KEYWORD, err.Error())
		n.cancel()
	default:
		// If pushing to fatalErrorFeedback would block, then we don't bother. Because we are using a buffered channel,
		// if we get here it means that the channel is busy already - one fatal error is as good as many.
		n.logger.Errorw("raft, skipped signalling fatal error, signalled already", rAFT_ERR_KEYWORD, err.Error())
	}
}

// NodeOption operator, operates on node to manage configuration.
type NodeOption func(*Node) error

// WithLogger option is invoked by the application to provide a customised zap logger option, or to disable logging.
// The NodeOption returned by WithLogger is passed in to MakeNode to control logging; e.g. to provide a preconfigured
// application logger. If logger passed in is nil, raft will disable logging.
//
// If WithLogger generated NodeOption is not passed in, package uses its own configured zap logger.
//
// Finally, if application wishes to derive its logger as some variant of the default raft logger, application can invoke
// DefaultZapLoggerConfig() to fetch a default logger configuration. It can use that configuration (modified as necessary)
// to build a new logger directly through zap library. That new logger can then be passed into WithLogger to generate
// the appropriate node option. An example of exactly this use case is available in the godoc examples. In the example,
// the logger configuration is set up to allow for on-the-fly changes to logging level.
//
// grpcLogs controls whether raft package redirects underlying gprc middleware logging to zap log. This is noisy, and
// unless in depth gRPC troubleshooting is required, grpcLogToZap should be set to false.
func WithLogger(logger *zap.Logger, grpcLogToZap bool) NodeOption {
	return func(n *Node) error {
		if logger != nil {
			n.logger = logger.Sugar()
		} else {
			n.logger = zap.NewNop().Sugar()
		}
		n.messaging.grpcLogging = grpcLogToZap

		return nil
	}
}

// WithMetrics option used with MakeNode to specify metrics registry we should count in. Detailed
// option indicates whether detailed (and more expensive) metrics are tracked (e.g. grpc latency distribution).
// If nil is passed in for the registry, the default registry prometheus.DefaultRegisterer is used. Do note that
// the package does not setup serving metrics; that is up to the application. The examples in godoc show how to
// setup a custom metrics registry to log against. If the WithMetrics NodeOption is passed in to MakeNode, metrics
// collection is disabled.
func WithMetrics(registry *prometheus.Registry, detailed bool) NodeOption {
	return func(n *Node) error {
		n.metrics = initMetrics(registry, detailed)
		return nil
	}
}

// Start raft node according to configuration provided.
//
// Node is returned, and public methods associated with Node can be used to interact with Node from multiple go
// routines e.g. specifically in order to access the replicated log.
//
// Context can be cancelled to signal exit. WaitGroup wg should have 1 added to it prior to calling MakeNode and
// should be waited on by the caller before exiting following cancellation. Whether MakeNode returns successfully ot
// not, WaitGroup will be marked Done() by the time the Node has cleaned up.
//
// If MakeNode returns without error, than over its lifetime it will be striving to maintain
// the node as a raft member in the raft cluster, and maintaining its replica of the replicated
// log.
//
// If a fatal error is encountered this will be signalled over the fatalError channel.
// A buffered channel of errors is provided to allow for raft package to signal fatal errors
// upstream and allow client to determine best course of action; typically close context to shutdown.
// As in the normal shutdown case, following receipt of a fatal error, caller should cancel context and wait
// for wait group before exiting. FatalErrorChannel method on the returned Node returns the error channel the
// application should consume.
//
// MakeNode also accepts logging and metrics options (see WithMetrics and WithLogger).
//
// For logging, we would like to support structured logging. This makes specifying a useful
// logger interface a little messier. Instead we depend on Uber zap and its interface, but allow user to
// either provide a configured zap logger of its own, allow raft to use its default logging setup,
// or disable logging altogether. Customisation is achieved through the WithLogging option.
//
// For metrics, raft package tries to adhere to the USE method as described here:
// (http://www.brendangregg.com/usemethod.html) - in summary 'For every resource, track  utilization, saturation
// and errors.'. On top of that we expect to be handed a metrics registry, and if one is provided, then we register
// our metrics against that. Metrics registry and customisation can be provided through the WithMetrics option.
//
func MakeNode(
	ctx context.Context,
	wg *sync.WaitGroup,
	cfg NodeConfig,
	opts ...NodeOption) (*Node, error) {

	defer wg.Done()

	err := cfg.validate()
	if err != nil {
		// We failed to initialise logging. We cannot log (obviously), so we simply return the error and
		// bail.
		return nil, err
	}

	n := &Node{
		messaging: &raftMessaging{},
		// A single fatal error is sufficient to do the job. Create buffered channel of 1. This matters,
		// because when we signal, where we to block, we would skip enqueuing signal on the basis we know at
		// least one signal is pending. And one signal would be enough.
		fatalErrorFeedback: make(chan error, 1),
		fatalErrorCount:    atomic.NewInt32(0),
	}

	for _, opt := range opts {
		err := opt(n)
		if err != nil {
			// It is too early and logging may not be setup yet. Simply return error.
			return nil, raftErrorf(RaftErrorBadMakeNodeOption, "applied option err [%v]", err)
		}
	}

	//
	// Setup a default discard logger to avoid test for every log entry.
	err = initLogging(n)
	if err != nil {
		// We failed to initialise logging. We cannot log (obviously), so we simply return the error and
		// bail.
		return nil, raftErrorf(err, "init logging failed")
	}

	n.logger.Info("raft package, starting up (logging can be customised or disabled using WithLogger option)")

	n.config = &cfg

	err = initMessaging(ctx, n)
	if err != nil {
		return nil, err
	}

	//
	// We are ready to run. We will allocate our own context. We do this in order to handle the owner shutdown
	// gracefully; specifically to orchestrate leadership transfer if we are leader on shutdown. Section 3.10 CBTP.
	rootCtx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel

	// Kick off messaging, remembering to add to the wait group. This ensures, that as long as client
	// honours wait group (i.e. waits on wait group on exit), then when exiting gracefully, the caller will
	// wait on the raft package to shut down (e.g. including leader transfer).
	wg.Add(1)

	// Use and internal workgroup we can wait on so we can clean up (e.g. flush the logger) on exit.
	var rootWg sync.WaitGroup
	rootWg.Add(1)
	runMessaging(rootCtx, &rootWg, n)

	// Wait for owner shutdown, wait for clean shutdown, then return.
	go func() {

		select {
		case <-rootCtx.Done():
			n.logger.Info("raft package internal shutdown triggered")

		case <-ctx.Done():
			n.logger.Info("raft package owner is requesting a shutdown")
		}

		fec := n.fatalErrorCount.Load()
		if fec == 0 {
			n.blockOnGracefulShutdown()
		} else {
			n.logger.Info("raft skipping graceful shutdown because we have taken fatal errors already")
		}

		// cancel() will signal exit to all the goroutines spawned by raft package. These will in turn mark wait group done
		// and let the owner eventually proceed.
		cancel()
		rootWg.Wait()
		// flush the logger to make sure we get all the logs
		n.logger.Sync()
		wg.Done()
	}()

	return n, nil
}

// Trigger graceful shutdown, and wait until this is complete.
func (n *Node) blockOnGracefulShutdown() {
	//
	// TODO: send message to trigger graceful shutdown to the main state machine.
	n.logger.Debugw("graceful shutdown, and triggering RequestTimeout is not implemented yet")
}

//
// DefaultZapLoggerConfig provides a production logger configuration (logs Info and above, JSON to stderr, with
// stacktrace, caller and sampling disabled) which can be customised by application to produce its own logger based
// on the raft configuration. Any logger provided by the application will also have its name extended by the raft
// package to clearly identify that log message comes from raft. For example, if the application log is named
// "foo", then the raft logs will be labelled with key "logger" value "foo.raft".
func DefaultZapLoggerConfig() zap.Config {

	lcfg := zap.NewProductionConfig()
	lcfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	lcfg.DisableStacktrace = false
	lcfg.DisableCaller = true
	lcfg.Sampling = nil

	return lcfg
}

// initLogging ensures that n.logger points at something even if it is pointing to a noop logger.
// By default, we log to an opinionated pre-configured log. The WithLog option can override configuration
// or disable logging completely.
func initLogging(n *Node) error {

	if n.logger == nil {
		logger, err := DefaultZapLoggerConfig().Build()
		if err != nil {
			return raftErrorf(err, "failed to set up logging")
		}
		n.logger = logger.Sugar()
	}

	// We must, absolutely must, never return without a logger and without an error.
	if n.logger == nil {
		return raftErrorf(
			RaftErrorMissingLogger, "tried to set up a logger, but failed, zap did not indicate why")
	}

	// Set logger name. This will end up being concatenated to a any preexisting log name. E.g. if the application
	// provide its log named 'myapp', then logger field in logs from raft will be 'myapp.raft'.
	n.logger = n.logger.Named("raft")

	//
	// Logger set up already - nothing we need too do.
	return nil
}
