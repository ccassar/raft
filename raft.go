package raft

import (
	"context"
	"errors"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"sync"
	"time"
)

// NodeConfig is the mandatory configuration for the local node. Every field in NodeConfig must be provided by
// application using the raft package. NodeConfig is passed in when starting up the node using MakeNode.
// (Optional configuration can be passed in using WithX NodeOption options).
type NodeConfig struct {
	// Raft cluster node addresses. Nodes should include all the node addresses including the local one, in the
	// form address:port. This makes configuration easy in that all nodes can share the same configuration.
	//
	// The order of the nodes (ignoring the local node) is also interpreted as the order of preference to transfer
	// leadership to in case we need to transfer. Note that this tie breaker only kicks in amongst nodes
	// which have a log matched to ours.
	Nodes []string
	//
	// Application provides a channel over which committed log commands are published for the application to consume.
	// The log commands are opaque to the raft package.
	LogCmds chan []byte
	//
	// LogDB points at file location which is the home of the persisted logDB.
	LogDB string
	//
	// Private fields below are set throught he WithX options passed in to make node.
	//
	// Pass in method which provides dial options to use when connecting as gRPC client with other nodes as servers.
	// Exposing this configuration allows application to determine whether, for example, to use TLS in raft exchanges.
	// The callback passes in the local node and remote node (as in Nodes above) for which we are setting up client
	// connection.
	clientDialOptionsFn func(local, remote string) []grpc.DialOption
	//
	// Pass in method which provides server side grpc options. These will be merged in with default options, with
	// default options overridden if provided in configuration. The callback passes in the local node.
	serverOptionsFn func(local string) []grpc.ServerOption
	//
	// Channel depths (optional). If not set, depths will default to sensible values.
	channelDepth struct {
		clientEvents int32
	}
	//
	// Configurable raft timers.
	timers struct {
		// leaderTimeout is used to determine the maximum period which will elapse without getting AppendEntry
		// messages from the leader. On the follower side, a random value between leaderTimeout and 2*leaderTimeout is
		// used by the raft package to determine when to force an election. On the leader side, leader will attempt
		// to send at least one AppendEntry (possible empty if necessary) within ever leaderTimeout period. Randomized
		// election timeouts minimise the probability of split votes and resolves them quickly when they happen as
		// described in Section 3.4 of CBTP. leaderTimeout defaults to 2s if not set.
		leaderTimeout time.Duration
	}
}

// NewNodeConfig returns a NodeConfig structure initialised with sensible defaults where possible. Caller,
// will need to setup Nodes as a minimum before using NodeConfig in MakeNode.
func NewNodeConfig() NodeConfig {

	nc := NodeConfig{}

	return nc
}

const minNodesInCluster = 3

// NodeConfig.validate: provides validation function for the configuration presented by user. Defaults are also
// set if necessary.
func (cfg *NodeConfig) validate(localNodeIndex int32) error {

	if len(cfg.Nodes) < minNodesInCluster {
		return raftErrorf(
			RaftErrorMissingNodeConfig,
			"not enough endpoints specified in Nodes %s, expect at least %d "+
				"e.g. 'n1.example.com:443','n3.example.com:443','n3.example.com:443'",
			cfg.Nodes, minNodesInCluster)
	}

	if cfg.LogCmds == nil {
		return raftErrorf(
			RaftErrorMissingNodeConfig,
			"missing LogCmds, a channel over which raft package will publish committed log commands")
	}

	if int32(len(cfg.Nodes)) <= localNodeIndex {
		return raftErrorf(
			RaftErrorBadLocalNodeIndex,
			"localNodeIndex specified %d is out of bounds for number endpoints specified in Nodes %d",
			localNodeIndex, len(cfg.Nodes))
	}

	// Default to insecure dial options (i.e. no underlying TLS)
	cfg.clientDialOptionsFn = func(l, r string) []grpc.DialOption {
		return []grpc.DialOption{grpc.WithInsecure()}
	}

	if cfg.timers.leaderTimeout == 0 {
		cfg.timers.leaderTimeout = time.Duration(2 * time.Second)
	}

	if cfg.channelDepth.clientEvents == 0 {
		cfg.channelDepth.clientEvents = 32
	}

	if cfg.LogDB == "" {
		return raftErrorf(
			RaftErrorMissingNodeConfig,
			"missing LogDB, a file name where local node persists both metadata and log entries")
	}

	return nil
}

// Node tracks the state and configuration of this local node. Public access to services provided by node are concurrency
// safe. Node structure carries the state of the local running raft instance.
type Node struct {
	// My index in the cluster. This attribute is set once we greedily find a node in NodeConfig.Nodes, for which we can
	// acquire a local socket.
	index int32
	// Readonly state provided when the Node is created.
	config *NodeConfig
	// raftEngine implements the raft state machine.
	engine *raftEngine
	// Server and client side state for messaging (independent of role). Messaging is implemented largely in raft_grpc.go.
	messaging *raftMessaging
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

	kv = append(kv, "obj", "localNode", "localNodeIndex", n.index, "clients", len(n.messaging.clients),
		"fatalErrorCount", n.fatalErrorCount.Load())

	return kv
}

// signalFatalError allows package to indicate fatal error to user. This will typically be followed by the client
// shutting down by cancelling context. If the buffered channel is full, we would just skip asking yet again.
func (n *Node) signalFatalError(err error) {

	n.fatalErrorCount.Inc()

	select {
	case n.fatalErrorFeedback <- err:
		n.logger.Errorw("raft, signalling fatal error", raftErrKeyword, err.Error())
		n.cancel()
	default:
		// If pushing to fatalErrorFeedback would block, then we don't bother. Because we are using a buffered channel,
		// if we get here it means that the channel is busy already - one fatal error is as good as many.
		n.logger.Errorw("raft, skipped signalling fatal error, signalled already", raftErrKeyword, err.Error())
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

// WithLeaderTimeout used with MakeNode to specify leader timeout: leader timeout is used to determine the maximum period
// which can elapse without getting AppendEntry messages from the leader without forcing a new election. On the follower
// side, a random value between leaderTimeout and 2*leaderTimeout is used by the raft package to determine when to force
// an election. On the leader side, leader will attempt to send at least one AppendEntry (possible empty if necessary)
// within ever leaderTimeout period. Randomized election timeouts minimise the probability of split votes and resolves
// them quickly when they happen as described in Section 3.4 of CBTP. LeaderTimeout defaults to 2s if not set.
func WithLeaderTimeout(leaderTimeout time.Duration) NodeOption {
	return func(n *Node) error {
		n.config.timers.leaderTimeout = leaderTimeout
		return nil
	}
}

// WithChannelDepthToClientOffload allows the application to overload the channel depth between the raft core engine,
// and the goroutine which handles messaging to the remote nodes. A sensible default value is used if this option is not
// specified.
func WithChannelDepthToClientOffload(depth int32) NodeOption {
	return func(n *Node) error {
		if depth <= 0 {
			return errors.New("bad channel depth in WithChannelDepthToClientOffload, must be > 0")
		}
		n.config.channelDepth.clientEvents = depth
		return nil
	}
}

// WithServerOptionsFn sets up callback used to allow application to specify server side grpc options for local raft
// node. These server options will be merged in with default options, with default options overwritten if provided
// by callback. The callback passes in the local node as specified in the Nodes configuration. Server side options
// could be used, for example, to set up mutually authenticated TLS protection of exchanges with other nodes.
func WithServerOptionsFn(fn func(local string) []grpc.ServerOption) NodeOption {
	return func(n *Node) error {
		n.config.serverOptionsFn = fn
		return nil
	}
}

// WithClientDialOptionsFn sets up callback used to allow application to specify client side grpc options for local
// raft node. These client options will be merged in with default options, with default options overwritten if provided
// by callback. The callback passes in the local/remote node pair in the form specified in the Nodes configuration.
// Client side options could be used, for example, to set up mutually authenticated TLS protection of exchanges
// with other nodes.
func WithClientDialOptionsFn(fn func(local, remote string) []grpc.DialOption) NodeOption {
	return func(n *Node) error {
		n.config.clientDialOptionsFn = fn
		return nil
	}
}

// MakeNode starts the raft node according to configuration provided.
//
// Node is returned, and public methods associated with Node can be used to interact with Node from multiple go
// routines e.g. specifically in order to access the replicated log.
//
// Context can be cancelled to signal exit. WaitGroup wg should have 1 added to it prior to calling MakeNode and
// should be waited on by the caller before exiting following cancellation. Whether MakeNode returns successfully ot
// not, WaitGroup will be marked Done() by the time the Node has cleaned up.
//
// The configuration block NodeConfig, along with localNodeIndex determine the configuration required to join the
// cluster. The localNodeIndex determines the identity of the local node as an index into the list of nodes in
// the cluster as specific in NodeConfig Nodes field.
//
// If MakeNode returns without error, than over its lifetime it will be striving to maintain
// the node as a raft member in the raft cluster, and maintaining its replica of the replicated
// log.
//
// If a fatal error is encountered at any point in the life of the node after MakeNode has returned, error will be
// signalled over the fatalError channel. A buffered channel of errors is provided to allow for raft package to signal
// fatal errors upstream and allow client to determine best course of action; typically close context to shutdown.
// As in the normal shutdown case, following receipt of a fatal error, caller should cancel context and wait
// for wait group before exiting. FatalErrorChannel method on the returned Node returns the error channel the
// application should consume.
//
// MakeNode also accepts various options including, gRPC server and dial options, logging and metrics (see functions
// returning NodeOption like WithMetrics, WithLogging etc).
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
	localNodeIndex int32,
	opts ...NodeOption) (*Node, error) {

	defer wg.Done()

	err := cfg.validate(localNodeIndex)
	if err != nil {
		// We failed to initialise logging. We cannot log (obviously), so we simply return the error and
		// bail.
		return nil, err
	}

	n := &Node{
		index:     localNodeIndex,
		config:    &cfg,
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

	err = initMessaging(ctx, n)
	if err != nil {
		return nil, err
	}

	err = initRaftEngine(ctx, n)
	if err != nil {
		return nil, err
	}

	//
	// We are ready to run. We will allocate our own context. We do this in order to handle the owner shutdown
	// gracefully; specifically to orchestrate leadership transfer if we are leader on shutdown. Section 3.10 CBTP.
	messagingCtx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel

	// Kick off messaging, remembering to add to the wait group. This ensures, that as long as client
	// honours wait group (i.e. waits on wait group on exit), then when exiting gracefully, the caller will
	// wait on the raft package to shut down (e.g. including leader transfer).
	wg.Add(1)

	// Use and internal workgroup we can wait on so we can clean up (e.g. flush the logger) on exit.
	var messagingWg sync.WaitGroup
	messagingWg.Add(1)
	runMessaging(messagingCtx, &messagingWg, n)

	engineCtx, engineCancel := context.WithCancel(context.Background())
	var engineWg sync.WaitGroup
	engineWg.Add(1)
	go n.engine.run(engineCtx, &engineWg, n)

	// Wait for owner shutdown, wait for clean shutdown, then return.
	go func() {

		select {
		case <-messagingCtx.Done():
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
		messagingWg.Wait()

		// we can now kill engineWg now that messageing is shutdown.
		engineCancel()
		engineWg.Wait()

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

func (n *Node) keepalivePeriod() time.Duration {
	return n.config.timers.leaderTimeout / 3
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

// initLogging ensures that node.logger points at something even if it is pointing to a noop logger.
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
