package raft

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/ccassar/raft/internal/raft_pb"
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
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMakeNode(t *testing.T) {

	// A neat property of the package is that it hunts for a node it can become - if cluster is made of of three
	// nodes, node config does not explicitly say which node it is - instead, node simply hunts and tries to listen
	// on any of the node sockets configured - if it succeeds, than it is the one. Identical configurations
	// for Nodes can be passed in to all nodes; e.g. these three local instances fired up will between them settle
	// in the role of one of the configured nodes.
	nodeCfgs := []NodeConfig{{
		Nodes:   []string{":8088", ":8089", ":8090"},
		LogDB:   "test/boltdb.8088",
		LogCmds: make(chan []byte),
		ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
			return []grpc.DialOption{grpc.WithInsecure()}
		}}, {
		Nodes:   []string{":8088", ":8089", ":8090"},
		LogDB:   "test/boltdb.8089",
		LogCmds: make(chan []byte),
		ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
			return []grpc.DialOption{grpc.WithInsecure()}
		}}, {
		Nodes:   []string{":8088", ":8089", ":8090"},
		LogDB:   "test/boltdb.8090",
		LogCmds: make(chan []byte),
		ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
			return []grpc.DialOption{grpc.WithInsecure()}
		}},
	}

	// Setup prometheus endpoint on default registry.
	http.Handle("/metrics", promhttp.Handler())
	promEndpoint := ":8000"
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
				LogCmds: make(chan []byte),
				ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
					return []grpc.DialOption{grpc.WithInsecure()}
				}}},
			[]NodeOption{},
			false,
			func(ctx context.Context, nodes []*Node) error { return nil },
		},
		{
			"NEGATIVE Node Config Missing Explicit Security Option",
			[]NodeConfig{{
				Nodes:               []string{":8088", ":8089", ":8090"},
				LogCmds:             make(chan []byte),
				ClientDialOptionsFn: nil}},
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
			[]NodeOption{WithMetrics(nil, true)},
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
					append(tc.opts, WithLogger(l.Named(fmt.Sprintf("LOG%d", i)), false))...)
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
				t.Log(testScrapeMetrics("http://:8000/metrics"))
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

	nc := NewNodeConfig()
	nc.Nodes = []string{":8080", ":8081", ":8082"}
	nc.LogCmds = make(chan []byte)
	nc.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		tlsCfg := &tls.Config{
			ServerName:   serverToName[remote],                   // server name,
			Certificates: []tls.Certificate{serverToCert[local]}, // client cert
			RootCAs:      caPool,
		}
		return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))}
	}
	nc.ServerOptionsFn = func(local string) []grpc.ServerOption {
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
		metricsReg[i], metricsServer[i] = testSetupMetricsRegistryAndServer(
			fmt.Sprintf(":%d", 8001+i), "/metrics")

		nc.LogDB = fmt.Sprintf("test/boltdb.%d", i)
		wg.Add(1)
		n, err := MakeNode(ctx, &wg, nc, int32(i),
			WithLogger(l.Named(fmt.Sprintf("LOG%d", i)), false),
			WithMetrics(metricsReg[i], true))

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
		t.Log(testScrapeMetrics(fmt.Sprintf("http://:%d/metrics", 8001+i)))
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

	l, err := DefaultZapLoggerConfig().Build()
	if err != nil {
		t.Error("failed to set up logging for test. ", err)
	}

	n := &Node{
		index:  1,
		logger: l.Sugar(),
		config: &NodeConfig{Nodes: []string{
			"1.2.3.4:12345",
			":8989", // we expect this to be picked based on index.
		}}}

	err = initClients(nil, n)
	if err == nil {
		t.Errorf("expected initClient on %s nodes to fail", n.config.Nodes[0])
	} else if errors.Cause(err) != RaftErrorServerNotSetup {
		t.Errorf("expected initClient on %s nodes to fail with %v, got %v",
			n.config.Nodes[0], RaftErrorServerNotSetup, err)
	}

	n.messaging = &raftMessaging{grpcLogging: false}
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

func TestElectionFollowerCandidate(t *testing.T) {

	l := testLoggerGet()

	nc := NodeConfig{
		Nodes:   []string{":8088", ":8089", ":8090"},
		LogCmds: make(chan []byte),
		ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
			return []grpc.DialOption{grpc.WithInsecure()}
		}}

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg.Add(1)
	_, err := MakeNode(ctx, &wg, nc, 0, WithLogger(l, false))
	if err != nil {
		t.Fatal(err)
	}

	//TODO Continue test
	time.Sleep(time.Second * 5)
	cancel()
	wg.Wait()

}

// ExampleMakeNode provides a simple example of how we kick off the Raft package,
// and also how we can programmatically handle errors if we prefer to. It also shows how
// asynchronous fatal errors in raft can be received and handled.
func ExampleMakeNode() {

	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())

	//
	// At a minimum, we need config to describe
	//  1. cluster nodes
	//  2. specify TLS or absence of it. We adopt the same stance as the grpc library;
	//     no TLS has to be explicitly requested and we do not default to it.
	//
	// Note how in this example we rely on the default zap logger set up by raft. This logging can
	// be disabled using  WithLogger(nil), or customised by specifying a logger instead of nil.
	cfg := NewNodeConfig()
	cfg.Nodes = []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"}
	cfg.LogCmds = make(chan []byte, 32)
	cfg.LogDB = "mydb.bbolt"
	cfg.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		return []grpc.DialOption{grpc.WithInsecure()}
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

	cfg := NewNodeConfig()
	cfg.Nodes = []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"}
	cfg.LogCmds = make(chan []byte, 32)
	cfg.LogDB = "mydb.bbolt"
	cfg.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		return []grpc.DialOption{grpc.WithInsecure()}
	}

	loggerCfg := DefaultZapLoggerConfig()
	logger, err := loggerCfg.Build( /* custom options can be provided here */ )
	if err != nil {
		//...
	}

	wg.Add(1)
	n, err := MakeNode(ctx, &wg, cfg, 2, WithLogger(logger, false))
	if err != nil {
		/// handle error
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
	nc := NewNodeConfig()
	nc.Nodes = []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"}
	nc.LogCmds = make(chan []byte, 32)
	nc.LogDB = "mydb.bbolt"
	nc.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		tlsCfg := &tls.Config{
			ServerName:   serverToName[remote],
			Certificates: []tls.Certificate{localCert},
			// If RootCAs is not set, host OS root CA set is used to validate server certificate.
			// Alternatively, custom Cert CA pool to use to validate server certificate would be set up here.
			RootCAs: certPool,
		}
		return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))}
	}
	nc.ServerOptionsFn = func(local string) []grpc.ServerOption {
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
	n, err := MakeNode(ctx, &wg, nc, 0, WithLogger(l, true))
	if err != nil {
		// handle err
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

	cfg := NewNodeConfig()
	cfg.Nodes = []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"}
	cfg.LogCmds = make(chan []byte, 32)
	cfg.LogDB = "mydb.bbolt"
	cfg.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		return []grpc.DialOption{grpc.WithInsecure()}
	}

	_, err = MakeNode(ctx, &wg, cfg, 1, // say if we are node2.example.com
		WithMetrics(nil, true),
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

	cfg := NewNodeConfig()
	cfg.Nodes = []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"}
	cfg.LogCmds = make(chan []byte, 32)
	cfg.LogDB = "mydb.bbolt"
	cfg.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		return []grpc.DialOption{grpc.WithInsecure()}
	}

	myregistry := prometheus.NewRegistry()
	// Do remember to serve metrics by setting up the server which serves the prometheus handler
	// obtained by handler := promhttp.HandlerFor(myregistry, promhttp.HandlerOpts{})

	_, err = MakeNode(ctx, &wg, cfg, 1, // say we are node2.example.com
		WithMetrics(myregistry, true),
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
			cl.eventChannel <- &e
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
			case cl.eventChannel <- &e:
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

func testScrapeMetrics(url string) string {

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Sprint("FAILED TO SCRAPE METRICS ", url, err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return fmt.Sprint("FAILED TO SCRAPE METRICS ", url, err)
	}
	return string(body)
}

func testSetupMetricsRegistryAndServer(endpoint, path string) (*prometheus.Registry, *http.Server) {

	// Setup prometheus endpoint on default registry.
	metricsReg := prometheus.NewRegistry()
	handler := promhttp.HandlerFor(metricsReg, promhttp.HandlerOpts{})

	handlerMux := http.NewServeMux()
	handlerMux.Handle(path, handler)
	metricServer := &http.Server{
		Addr:    endpoint,
		Handler: handlerMux,
	}
	go metricServer.ListenAndServe()

	return metricsReg, metricServer
}

func testLoggerGet() *zap.Logger {

	loggerCfg := DefaultZapLoggerConfig()
	loggerCfg.Level.SetLevel(zapcore.DebugLevel)
	logger, _ := loggerCfg.Build()

	return logger
}
