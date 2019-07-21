package raft

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/ccassar/raft/internal/raft_pb"
	"github.com/cenkalti/backoff"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const testMetricsBasePort = 9001
const testMetricNamespace = "raftTest"

// Readonly node ports...
var constNodes = []string{":8089", ":8090", ":8092"}

func TestMakeNode(t *testing.T) {

	// A neat property of the package is that it hunts for a node it can become - if cluster is made of of three
	// nodes, node config does not explicitly say which node it is - instead, node simply hunts and tries to listen
	// on any of the node sockets configured - if it succeeds, than it is the one. Identical configurations
	// for Nodes can be passed in to all nodes; e.g. these three local instances fired up will between them settle
	// in the role of one of the configured nodes.
	nodeCfgs := []NodeConfig{{
		Nodes:   constNodes,
		LogDB:   "test/boltdb.a",
		LogCmds: make(chan []byte)}, {
		Nodes:   constNodes,
		LogDB:   "test/boltdb.b",
		LogCmds: make(chan []byte)}, {
		Nodes:   constNodes,
		LogDB:   "test/boltdb.c",
		LogCmds: make(chan []byte)},
	}

	// Setup prometheus endpoint on default registry.
	http.Handle("/metrics", promhttp.Handler())
	promEndpoint := ":8002"
	go http.ListenAndServe(promEndpoint, nil)

	testCases := []struct {
		name string
		cfg  []NodeConfig
		opts []NodeOption
		ok   bool
		eval func(ctx context.Context, nodes []*Node) error
	}{
		{
			"NEGATIVE Cluster of two",
			[]NodeConfig{{
				Nodes:   []string{":8088", ":8089"},
				LogCmds: make(chan []byte)}},
			[]NodeOption{},
			false,
			func(ctx context.Context, nodes []*Node) error { return nil },
		},
		{
			"NEGATIVE Node Config Missing LogDB",
			[]NodeConfig{{
				Nodes:   constNodes,
				LogCmds: make(chan []byte)}},
			[]NodeOption{},
			false,
			func(ctx context.Context, nodes []*Node) error { return nil },
		},
		{
			"NEGATIVE Node Config Missing LogCmds channel",
			[]NodeConfig{{
				Nodes: constNodes,
				LogDB: "mydb"}},
			[]NodeOption{},
			false,
			func(ctx context.Context, nodes []*Node) error { return nil },
		},
		{
			"Cluster of three",
			nodeCfgs,
			[]NodeOption{},
			true,
			evalConnectedClients,
		},
		{
			"Exercise signalFatalError",
			nodeCfgs,
			[]NodeOption{},
			true,
			func(ctx context.Context, nodes []*Node) error {
				// Make sure we do not block event if channel is not drained.
				for i := 0; i < 3; i++ {

					// force signal failure with bad updates to current leader
					nodes[0].engine.updateCurrentLeader(-1)
					nodes[0].engine.updateCurrentLeader(2)
					nodes[0].engine.updateCurrentLeader(1)
					// Exercise the dampening showing we have taken fatal error already
					nodes[0].signalFatalError(fmt.Errorf("testing signal fatal error %d", i))
				}
				errChan := nodes[0].FatalErrorChannel()
				select {
				case <-errChan:
				case <-time.After(time.Second):
					return errors.New("failed to signal shutdown in time")
				}
				ctx.Done()
				return nil
			},
		},
		{
			"Exercise WithMetrics option(default registry)",
			nodeCfgs,
			[]NodeOption{WithMetrics(nil, "", true)},
			true,
			evalForApplicationLoopback,
		},
	}

	l := getTestLogger()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			var wg sync.WaitGroup
			ctx, cancel := context.WithCancel(context.Background())
			nodes := []*Node{}

			for i := range tc.cfg {
				wg.Add(1)
				n, err := MakeNode(ctx, &wg, tc.cfg[i], int32(i),
					append(tc.opts, WithLogger(l, false))...)
				if (err == nil) != tc.ok {
					t.Fatalf("%s, expected [%t], got [%v]", tc.name, tc.ok, err)
				}
				nodes = append(nodes, n)
			}

			err := tc.eval(ctx, nodes)
			if err != nil {
				t.Error(err)
			}

			if tc.ok { // no point scraping metrics if we do not expect MakeNode to succeed.
				t.Log("Metrics from all node")
				t.Log(testScrapeMetrics(1, "/metrics"))
			}

			cancel()
			wg.Wait()
		})
	}
}

// Test mutual authentication between clients and servers. We set up three nodes, and a full mesh of mutually
// authenticated TLS client to server session. We use self signed certificates. Simply replace (or omit) RootCAs
// for clients which are using certificates signed by a CA (if omitted, OS configured CA would be used).
func TestMakeNode_withTLSMutualProtection(t *testing.T) {

	caPool, err := testLoadCertPool("test")
	if err != nil {
		t.Fatal("loading CA cert pool failed:", err)
	}

	serverToCert := map[string]tls.Certificate{}
	serverToCert[":8080"], err = tls.LoadX509KeyPair("test/server0.crt", "test/server0.key")
	if err != nil {
		t.Fatal("failed to load server0 cert:", err)
	}

	serverToCert[":8081"], err = tls.LoadX509KeyPair("test/server1.crt", "test/server1.key")
	if err != nil {
		t.Fatal("failed to load server1 cert:", err)
	}

	serverToCert[":8082"], err = tls.LoadX509KeyPair("test/server2.crt", "test/server2.key")
	if err != nil {
		t.Fatal("failed to load server2 cert:", err)
	}

	serverToName := map[string]string{
		":8080": "server0",
		":8081": "server1",
		":8082": "server2",
	}

	nc := NodeConfig{
		Nodes:   []string{":8080", ":8081", ":8082"},
		LogCmds: make(chan []byte),
	}

	clientDialOptionsFn := func(local, remote string) []grpc.DialOption {
		tlsCfg := &tls.Config{
			ServerName:   serverToName[remote],                   // server name,
			Certificates: []tls.Certificate{serverToCert[local]}, // client cert
			RootCAs:      caPool,
		}
		return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))}
	}
	serverOptionsFn := func(local string) []grpc.ServerOption {
		tlsCfg := &tls.Config{
			ClientAuth:   tls.RequireAndVerifyClientCert,
			Certificates: []tls.Certificate{serverToCert[local]},
			ClientCAs:    caPool,
		}
		return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}
	}

	l := getTestLogger()

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	nodes := []*Node{}
	metricsReg := make([]*prometheus.Registry, len(nc.Nodes))
	metricsServer := make([]*http.Server, len(nc.Nodes))

	for i := 0; i < len(nc.Nodes); i++ {
		metricsReg[i], metricsServer[i] = testSetupMetricsRegistryAndServer(int32(i), "/metrics")

		nc.LogDB = fmt.Sprintf("test/boltdb.%d", i)
		wg.Add(1)
		n, err := MakeNode(ctx, &wg, nc, int32(i),
			WithClientDialOptionsFn(clientDialOptionsFn),
			WithServerOptionsFn(serverOptionsFn),
			WithLogger(l.Named(fmt.Sprintf("LOG%d", i)), false),
			WithMetrics(metricsReg[i], "", true))

		if err != nil {
			t.Fatalf("%s, expected ok, got [%v]", t.Name(), err)
		}
		nodes = append(nodes, n)
	}

	err = evalForApplicationLoopback(ctx, nodes)
	if err != nil {
		t.Error(err)
	}

	for i := 0; i < len(nc.Nodes); i++ {
		t.Log("Metrics from node: ", nodes[i].logKV())
		t.Log(testScrapeMetrics(int32(i), "/metrics"))
		metricsServer[i].Shutdown(ctx)
	}

	cancel()
	wg.Wait()

}

func TestInitLogging(t *testing.T) {

	l, err := DefaultZapLoggerConfig().Build()
	if err != nil {
		t.Fatal(err)
	}
	l.Info("log setup")

	//
	// Test logger without logs.
	n := Node{
		messaging: &raftMessaging{},
	}
	err = initLogging(&n)
	if err != nil {
		t.Errorf("expect initLogging to not fail [%v]", err)
	}

	n.logger.Info("logging with default logger config")
	if n.logger == nil {
		t.Error("initLogging returns without error AND without logger set")
	}

	// exercise disable logging - WithLogger is passed into MakeNode by applications.
	// Here we exercise the internals.
	f := WithLogger(nil, false)
	err = f(&n)
	if err != nil {
		t.Errorf("init logging failed with [%v] to apply WithLogger to node", err)
	}
	err = initLogging(&n)
	if err != nil {
		t.Errorf("expect initLogging for noop logging to not fail [%v]", err)
	}

	n.logger.Info("THIS SHOULD NOT BE SEEN, LOGS SHOULD BE DISCARDED")

}

func TestInitMessaging(t *testing.T) {

	n := &Node{
		index:           1,
		logger:          testLoggerGet().Sugar(),
		fatalErrorCount: atomic.NewInt32(0),
		config: &NodeConfig{Nodes: []string{
			"1.2.3.4:12345",
			":8989", // we expect this to be picked based on index.
		}}}

	err := initClients(nil, n)
	if err == nil {
		t.Errorf("expected initClient on %s nodes to fail", n.config.Nodes[0])
	} else if errors.Cause(err) != RaftErrorServerNotSetup {
		t.Errorf("expected initClient on %s nodes to fail with %v, got %v",
			n.config.Nodes[0], RaftErrorServerNotSetup, err)
	}

	n.messaging = &raftMessaging{}
	err = initMessaging(nil, n)
	if err != nil {
		t.Errorf("expected socket on %s to open, but failed [%v]",
			n.config.Nodes[0], err)
	}
	t.Logf("opened local socket on %v\n", n.messaging.server.localListener.Addr())

	err = initMessaging(nil, n)
	if err == nil {
		t.Errorf("expected %s to fail to open but it did", n.config.Nodes[0])
	} else {
		t.Logf("ERROR AS EXPECTED (%v)", err)
	}

}

// Exercise preferred error generation
func TestWrapperErrorRendering(t *testing.T) {
	err := raftErrorf(
		RaftErrorBadMakeNodeOption, "testing error and sentinel, [%v,%v]",
		37, 64)
	fmt.Println("normal rendering: ", err)
	fmt.Printf("detail rendering: %+v\n", err)
}

type testNode struct {
	nc     NodeConfig
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	node   *Node
}

func testAddNode(nodes []string, i int, electionPeriod time.Duration) (*testNode, error) {
	return testAddNodeWithDB(nodes, i, "", electionPeriod)
}

func testAddNodeWithDB(nodes []string, i int, db string, electionPeriod time.Duration) (*testNode, error) {

	var err error

	var tn testNode

	l := testLoggerGet()

	if db == "" {
		db = fmt.Sprintf("test/boltdb.%d", i)
	}

	tn.nc = NodeConfig{
		Nodes:   nodes,
		LogDB:   db,
		LogCmds: make(chan []byte),
	}

	tn.ctx, tn.cancel = context.WithCancel(context.Background())

	registry, metricsServer := testSetupMetricsRegistryAndServer(int32(i), "/metrics")
	go func() {
		<-tn.ctx.Done()
		metricsServer.Shutdown(context.Background())
	}()

	tn.wg.Add(1)
	tn.node, err = MakeNode(tn.ctx, &tn.wg, tn.nc, int32(i),
		WithLogger(l, false),
		WithLeaderTimeout(electionPeriod),
		WithUnaryGRPCTimeout(electionPeriod>>1), // default
		WithLogCommandBatchSize(48),
		WithChannelDepthToClientOffload(64),
		WithMetrics(registry, testMetricNamespace, false))

	return &tn, err
}

func TestDetectBlockedBoltDB(t *testing.T) {
	var err error
	n := make([]*testNode, 2)

	db := "test/boltdbNEG.0"
	nodes := []string{":8188", ":8189", ":8190"}
	n[0], err = testAddNodeWithDB(nodes, 0, db, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	n[1], err = testAddNodeWithDB(nodes, 1, db, time.Second)
	if err == nil {
		t.Fatal("expected reused bbolt DB to force an error")
	}

	if errors.Cause(err).Error() != "timeout" {
		t.Fatal("expected reused bbolt DB to force an error, but error returned is not expected one", err)
	}

	for i := 0; i < 2; i++ {
		if n[i] != nil && n[i].cancel != nil {
			n[i].cancel()
			n[i].wg.Wait()
		}
	}
}

func TestElection(t *testing.T) {

	electionPeriod := time.Millisecond * 400
	wait := electionPeriod * 30
	cycles := 5
	nodeCount := 3
	for i := 0; i < nodeCount; i++ {
		os.Remove(fmt.Sprintf("test/boltdb.%d", i))
	}

	n := make([]*testNode, nodeCount)
	nodes := constNodes
	var err error

	for cycle := 0; cycle < cycles; cycle++ {

		// Bring up the first node.
		fmt.Println(" ***************  CYCLE START: Bring up 0")
		n[0], err = testAddNode(nodes, 0, electionPeriod)
		if err != nil {
			t.Fatal(err)
		}

		fmt.Println(" ***************  Bring up 1")
		n[1], err = testAddNode(nodes, 1, electionPeriod)
		if err != nil {
			t.Fatal(err)
		}

		whoIsLeader := testFindNewLeader(n, wait, true)
		if whoIsLeader == noLeader {
			t.Fatal("expected leader but did not get one")
		}

		//
		// Bring up new node...
		fmt.Println(" ***************  Bring up 2")
		n[2], err = testAddNode(nodes, 2, electionPeriod)
		if err != nil {
			t.Fatal(err)
		}

		whoIsLeader = testFindNewLeader(n, wait, false)
		if whoIsLeader == noLeader {
			t.Fatal("expected leader but did not get one")
		}

		fmt.Println(" ***************  Kill the current leader", whoIsLeader)
		n[whoIsLeader].cancel()
		n[whoIsLeader].cancel = nil
		n[whoIsLeader].wg.Wait()

		inNeedOfResucitation := whoIsLeader
		whoIsLeader = testFindNewLeader(n, wait, true)
		if whoIsLeader == noLeader {
			t.Fatal("expected leader but did not get one")
		}

		fmt.Println(" ***************  Bring up ", inNeedOfResucitation)
		n[inNeedOfResucitation], err = testAddNode(nodes, inNeedOfResucitation, electionPeriod)
		if err != nil {
			t.Fatal(err)
		}

		whoIsLeader = testFindNewLeader(n, wait, false)
		if whoIsLeader == noLeader {
			t.Fatal("expected leader but did not get one")
		}

		// Take two nodes out...
		takeDown := []int{whoIsLeader, whoIsLeader + 1}
		if takeDown[1] >= len(n) {
			takeDown[1] = 0
		}

		fmt.Println(" ***************  Kill majority including leader", takeDown)
		for i := range takeDown {
			n[i].cancel()
			n[i].cancel = nil
		}

		fmt.Println(" ***************  Restart killed nodes", takeDown)
		for i := range takeDown {
			n[i].wg.Wait() // wait for previous instance to finish coming down..., before we restart it.
			n[i], err = testAddNode(nodes, i, electionPeriod)
			if err != nil {
				t.Fatal(err)
			}
		}

		whoIsLeader = testFindNewLeader(n, wait, false)
		if whoIsLeader == noLeader {
			t.Fatal("expected leader but did not get one")
		}

		for i := 0; i < nodeCount; i++ {
			if n[i] == nil {
				continue
			}
			if n[i].cancel != nil {
				fmt.Println("Cancelling: ", i)
				n[i].cancel()
			}
			fmt.Println("Waiting for: ", i)
			n[i].wg.Wait()
		}
	}
}

// ExampleMakeNode provides a simple example of how we kick off the Raft package,
// and also how we can programmatically handle errors if we prefer to. It also shows how
// asynchronous fatal errors in raft can be received and handled.
func ExampleMakeNode() {

	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())

	cfg := NodeConfig{
		Nodes:   []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"},
		LogCmds: make(chan []byte, 32),
		LogDB:   "mydb.bbolt",
	}
	wg.Add(1)
	localIndex := int32(2) // say, if we are node3.example.com
	n, err := MakeNode(ctx, &wg, cfg, localIndex)
	if err != nil {

		switch errors.Cause(err) {
		case RaftErrorBadMakeNodeOption:
			//
			// Handle specific sentinel in whichever way we see fit.
			// ...
		default:
			// Root cause is not a sentinel.
		}
		// err itself renders the full context not just the context sentinel.
		fmt.Println(err)

	} else {

		fmt.Printf("node started with config [%v]", n.config)

		// Handle any fatal signals from below as appropriate... either by starting a new instance of exiting and letting
		// orchestrator handle failure.
		fatalSignal := n.FatalErrorChannel()

		//...
		// Once we are done, we can signal shutdown and wait for raft to clean up and exit.
		select {
		case err := <-fatalSignal:
			// handle fatal error as appropriate.
			fmt.Println(err)

		case <-ctx.Done():
			//...
		}

	}

	cancel()
	wg.Wait()
}

// ExampleMakeNodeWithCustomisedLogLevel provides a simple example of how we kick off the Raft package,
// with a logger provided by application. The application chooses to base its log configuration on the default raft log
// configuration, and tweaks that configuration with on-the-fly logging level setting. Finally, application also requests
// that raft package redirects underlying grpc package logging to zap.
func ExampleMakeNode_withCustomisedLogLevel() {

	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())

	cfg := NodeConfig{
		Nodes:   []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"},
		LogCmds: make(chan []byte, 32),
		LogDB:   "mydb.bbolt",
	}

	loggerCfg := DefaultZapLoggerConfig()
	logger, err := loggerCfg.Build( /* custom options can be provided here */ )
	if err != nil {
		//...
	}

	wg.Add(1)
	n, err := MakeNode(ctx, &wg, cfg, 2,
		WithLogger(logger, false),
		WithLeaderTimeout(time.Second))
	if err != nil {
		/// handle error
		return
	}

	//
	// At any point, the logging level can be safely and concurrently changed.
	loggerCfg.Level.SetLevel(zapcore.InfoLevel)

	fmt.Printf("node started with config [%v]", n.config)
	//...
	// Once we are done, we can signal shutdown and wait for raft to clean up and exit.
	cancel()
	wg.Wait()
}

// ExampleMakeNodeWithTLS is a simple example showing how TLS protection with mutual authentication can be setup
// between raft cluster nodes.
func ExampleMakeNode_withTLSConfiguration() {

	// Used when node operates as client to validate remote node name (as provided in Nodes) to certificate Common
	// Name.
	serverToName := map[string]string{
		"node1.example.com:443": "node1",
		"node2.example.com:443": "node2",
		"node3.example.com:443": "node3",
	}

	certPool := x509.NewCertPool()
	// Populate the cert pool with root CAs which can validate server and client certs. e.g.
	c, err := ioutil.ReadFile("rootCA.pem")
	if err != nil {
		// handle error
	}
	certPool.AppendCertsFromPEM(c)

	localCert, err := tls.LoadX509KeyPair("localnode.crt", "localnode.key")
	if err != nil {
		// handle error
	}

	// We setup a configuration to enforce authenticating TLS client connecting to this node, and to validate
	// server certificate in all client connections to remove cluster nodes.
	nc := NodeConfig{
		Nodes:   []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"},
		LogCmds: make(chan []byte, 32),
		LogDB:   "mydb.bbolt",
	}

	clientDialOptionsFn := func(local, remote string) []grpc.DialOption {
		tlsCfg := &tls.Config{
			ServerName:   serverToName[remote],
			Certificates: []tls.Certificate{localCert},
			// If RootCAs is not set, host OS root CA set is used to validate server certificate.
			// Alternatively, custom Cert CA pool to use to validate server certificate would be set up here.
			RootCAs: certPool,
		}
		return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))}
	}

	serverOptionsFn := func(local string) []grpc.ServerOption {
		tlsCfg := &tls.Config{
			ClientAuth:   tls.RequireAndVerifyClientCert,
			Certificates: []tls.Certificate{localCert},
			// If ClientCAs is not set, host OS root CA set is used to validate client certificate.
			// Alternatively, custom Cert CA pool to use to validate server certificate would be set up here.
			// ClientCAs pool does NOT need to be the same as RootCAs pool.
			ClientCAs: certPool,
		}
		return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}
	}

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	l, err := DefaultZapLoggerConfig().Build()
	if err != nil {
		// handle err
	}

	wg.Add(1)
	// if we are starting up node1.example.com, index would be 0
	n, err := MakeNode(ctx, &wg, nc, 0,
		WithClientDialOptionsFn(clientDialOptionsFn),
		WithServerOptionsFn(serverOptionsFn),
		WithLogger(l, true))
	if err != nil {
		// handle err
		return
	}

	fmt.Printf("node started with config [%v]", n.config)

	cancel()
	wg.Wait()

}

func ExampleMakeNode_withDefaultMetricsRegistry() {

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	l, err := DefaultZapLoggerConfig().Build()
	if err != nil {
		// handle err
	}

	cfg := NodeConfig{
		Nodes:   []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"},
		LogCmds: make(chan []byte, 32),
		LogDB:   "mydb.bbolt",
	}
	_, err = MakeNode(ctx, &wg, cfg, 1, // say if we are node2.example.com
		WithMetrics(nil, "appFoo", true),
		WithLogger(l, false))
	if err != nil {
		// handle error...
	}

	// Do remember to serve the metrics registered with the prometheus DefaultRegistry:
	// e.g. as described here: https://godoc.org/github.com/prometheus/client_golang/prometheus

	cancel()
	wg.Wait()
}

func ExampleMakeNode_withDedicatedMetricsRegistry() {

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	l, err := DefaultZapLoggerConfig().Build()
	if err != nil {
		// handle err
	}

	cfg := NodeConfig{
		Nodes:   []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"},
		LogCmds: make(chan []byte, 32),
		LogDB:   "mydb.bbolt",
	}

	myregistry := prometheus.NewRegistry()
	// Do remember to serve metrics by setting up the server which serves the prometheus handler
	// obtained by handler := promhttp.HandlerFor(myregistry, promhttp.HandlerOpts{})

	_, err = MakeNode(ctx, &wg, cfg, 1, // say we are node2.example.com
		WithMetrics(myregistry, "appFoo", true),
		WithLogger(l, false))
	if err != nil {
		/// handle error
	}

	cancel()
	wg.Wait()
}

func getTestLogger() *zap.Logger {
	cfg := DefaultZapLoggerConfig()
	// Switch to human readable logs for test.
	cfg.Encoding = "console"
	cfg.DisableStacktrace = true
	cfg.Level.SetLevel(zapcore.DebugLevel)
	l, _ := cfg.Build()
	return l
}

func testLoadCertPool(dir string) (*x509.CertPool, error) {
	fileInfos, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	certPool := x509.NewCertPool()
	for _, fInfo := range fileInfos {
		f := filepath.Join(dir, fInfo.Name())
		if filepath.Ext(f) == ".crt" {
			c, err := ioutil.ReadFile(f)
			if err != nil {
				return nil, err
			}
			if !certPool.AppendCertsFromPEM(c) {
				return nil, fmt.Errorf("failed to load cert %s into cert pool", f)
			}
		}

	}

	subjects := certPool.Subjects()
	if len(subjects) == 0 {
		return nil, errors.New("certPool empty; certs required")
	}

	return certPool, nil
}

type testEventApplicationLoopback struct {
	request  *raft_pb.AppNonce
	response *raft_pb.AppNonce
	err      error
	client   *raftClient
	done     chan struct{}
}

func (t *testEventApplicationLoopback) handle(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, time.Second*3)
	defer cancel()

	t.response, t.err = t.client.grpcClient.ApplicationLoopback(callCtx, t.request)
	if t.err == nil {
		if t.response.Nonce != t.request.Nonce {
			t.err = fmt.Errorf("mismatched nonces; out %v, in %v",
				t.request.Nonce, t.response.Nonce)
		}
	}
	close(t.done)
}

func (t *testEventApplicationLoopback) logKV() []interface{} {
	return append([]interface{}{"obj", "testEventApplicationLoopback", "request", *t.request}, t.client.logKV()...)
}

func evalForApplicationLoopback(ctx context.Context, nodes []*Node) error {

	for _, n := range nodes {

		for _, cl := range n.messaging.clients {

			e := testEventApplicationLoopback{
				client:  cl,
				done:    make(chan struct{}),
				request: &raft_pb.AppNonce{Nonce: 9898989}}
			cl.eventChan.channel <- &e
			select {
			case <-time.After(time.Second * 5):
				err := fmt.Errorf("ApplicationLoopback to %s timed out", cl.remoteAddress)
				return err
			case <-e.done:
			}
			if e.err != nil {
				return fmt.Errorf("ApplicationLoopback to %s failed %v", cl.remoteAddress, e.err)
			}

		}
	}
	return nil
}

type testEvent struct {
	name  string
	count *atomic.Int32
	wg    *sync.WaitGroup
}

func (t *testEvent) handle(ctx context.Context) {
	t.count.Inc()
	t.wg.Done()
}

func (t *testEvent) logKV() []interface{} {
	hits := t.count.Load()
	return []interface{}{"testEvent", t.name, "testEventHits", hits}
}

func (t *testEvent) waitForHitsOrTimeout(count int32, waitFor time.Duration) bool {
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitFor):
	}
	return t.count.Load() == count
}

func evalConnectedClients(ctx context.Context, nodes []*Node) error {
	// Number of events we expect for full mesh of connectivity between nodes: node-1.
	expect := len(nodes) - 1
	for _, n := range nodes {
		e := testEvent{name: "checkConn", count: atomic.NewInt32(0), wg: new(sync.WaitGroup)}
		e.wg.Add(expect)
		for _, cl := range n.messaging.clients {
			// Identify whether we got past blocking connection for client by posting an event.
			// On each node, if every connection is up, we should see expect number of hits on event.
			select {
			case cl.eventChan.channel <- &e:
			case <-time.After(time.Second):
				return fmt.Errorf("At node %d, connection %d failed with timeout posting event",
					n.index, cl.index)
			}
		}
		eventsRxed := e.waitForHitsOrTimeout(int32(expect), time.Second*5)
		if !eventsRxed {
			return fmt.Errorf("At node %d, expected %d events, and got %d",
				n.index, expect, e.count.Load())
		}
	}
	return nil
}

func testScrapeMetrics(index int32, path string) string {

	url := fmt.Sprintf("http://localhost:%d%s", testMetricsBasePort+index, path)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Sprintf("FAILED TO GET METRICS url %s, err %v", url, err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return fmt.Sprintf("GET METRICS FROM %s RECEIVED ERROR %v", url, err)
	}
	return string(body)
}

// Manual parsing of results looking for metrics of interest.
type scrapedMetric struct {
	name   string
	val    string
	labels string
}

var testImmutableMetricsRegexp = regexp.MustCompile(`^([a-zA-Z0-9_:]*){([a-zA-Z0-9_:="]*)} (\w+)$`)

func testScrapeMetricsAndExtract(index int32, path string, metrics []string) []*scrapedMetric {

	metricsMap := map[string]bool{}
	for _, m := range metrics {
		metricsMap[m] = true
	}

	out := testScrapeMetrics(index, path)
	lines := strings.Split(out, "\n")
	var result []*scrapedMetric
	for _, line := range lines {
		// fmt.Println(line)
		loc := testImmutableMetricsRegexp.FindSubmatch([]byte(line))
		if len(loc) == 4 {
			// (full match) metricName, labels, value
			_, ok := metricsMap[string(loc[1])]
			if ok {
				result = append(result, &scrapedMetric{
					name:   string(loc[1]),
					val:    string(loc[3]),
					labels: string(loc[2]),
				})
			}
		}
	}
	return result
}

func testScrapeOneMetricAndExtract(index int32, path string, metric string) *scrapedMetric {

	out := testScrapeMetrics(index, path)
	lines := strings.Split(out, "\n")

	for _, line := range lines {
		loc := testImmutableMetricsRegexp.FindSubmatch([]byte(line))
		if len(loc) == 4 {
			// fmt.Println(string(loc[0]))
			// (full match) metricName, labels, value
			if string(loc[1]) == metric {
				return &scrapedMetric{
					name:   string(loc[1]),
					val:    string(loc[3]),
					labels: string(loc[2]),
				}
			}
		}
	}

	return nil
}

func testFindNewLeader(n []*testNode, duration time.Duration, majorityEnough bool) int {
	var whoIsLeader int

	done := make(chan struct{})
	go func() {
		<-time.After(duration)
		close(done)
	}()
	bail := func() bool {
		select {
		case <-done:
			return true
		default:

		}
		return false
	}

	for {
		leader := 0
		for i := 0; i < len(n); i++ {
			if n[i] != nil {
				results := testScrapeMetricsAndExtract(int32(i), "/metrics",
					[]string{fmt.Sprintf("%s_raft_role", testMetricNamespace)})
				if len(results) == 1 {
					if results[0].val == "3" {
						whoIsLeader = i
						leader++
					}
				}
			}
		}
		if leader != 1 {
			time.Sleep(time.Second)
			if bail() {
				return noLeader
			}
			continue
		}

		//
		// Now that we think we have a leader, let's see if the majority concur
		leaderFollowers := 0
		for i := 0; i < len(n); i++ {
			if n[i] != nil {
				result := testScrapeOneMetricAndExtract(int32(i), "/metrics",
					fmt.Sprintf("%s_raft_leader", testMetricNamespace))
				if result != nil {
					if result.val == fmt.Sprintf("%d", whoIsLeader) {
						leaderFollowers++
					}
				}
			}
		}
		targetFollowers := len(n) - 1 // > test implies we have to have all of them.
		if majorityEnough {
			targetFollowers = len(n) >> 1
		}
		if leaderFollowers > targetFollowers {
			break // we're done; we converged to 1 node
		}
		time.Sleep(time.Second)
		if bail() {
			fmt.Println("Test LEADER NOT FOUND")
			return noLeader
		}
	}

	fmt.Println("Test found LEADER: ", whoIsLeader)
	return whoIsLeader
}

func testSetupMetricsRegistryAndServer(index int32, path string) (*prometheus.Registry, *http.Server) {

	endpoint := fmt.Sprintf("localhost:%v", testMetricsBasePort+index)

	// Setup prometheus endpoint on new registry.
	metricsReg := prometheus.NewRegistry()
	handler := promhttp.HandlerFor(metricsReg, promhttp.HandlerOpts{})

	handlerMux := http.NewServeMux()
	handlerMux.Handle(path, handler)
	metricServer := &http.Server{
		Addr:    endpoint,
		Handler: handlerMux,
	}

	go func() {
		err := backoff.Retry(func() error {
			err := metricServer.ListenAndServe()
			if err != nil && err != http.ErrServerClosed {
				return err
			}
			return nil
		}, backoff.WithMaxRetries(backoff.NewConstantBackOff(time.Millisecond*5000), 3))
		if err != nil {
			panic(err)
		}
	}()

	return metricsReg, metricServer
}

func testLoggerGet() *zap.Logger {

	loggerCfg := DefaultZapLoggerConfig()
	loggerCfg.Encoding = "console"
	loggerCfg.Level.SetLevel(zapcore.DebugLevel)
	logger, _ := loggerCfg.Build()

	return logger
}
