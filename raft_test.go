package raft

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/pkg/errors"
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"io/ioutil"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestMakeNode(t *testing.T) {

	testCases := []struct {
		name string
		cfg  []NodeConfig
		opts []NodeOption
		ok   bool
		eval func(ctx context.Context, nodes []*Node) error
	}{
		{
			"Cluster of three",
			// A neat property of the package is that it hunts for a node it can be. This way, identical configurations
			// for Nodes can be passed in, and the three local instances fired up will between them settle in the role
			// of one of the configured nodes.
			[]NodeConfig{{
				Nodes: []string{":8088", ":8089", ":8090"},
				ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
					return []grpc.DialOption{grpc.WithInsecure()}
				}}, {
				Nodes: []string{":8088", ":8089", ":8090"},
				ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
					return []grpc.DialOption{grpc.WithInsecure()}
				}}, {
				Nodes: []string{":8088", ":8089", ":8090"},
				ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
					return []grpc.DialOption{grpc.WithInsecure()}
				}},
			},
			[]NodeOption{},
			true,
			evalConnectedClients,
		},
		{
			"Exercise signalFatalError",
			[]NodeConfig{{
				Nodes: []string{":8088", ":8089", ":8090"},
				ClientDialOptionsFn: func(l, r string) []grpc.DialOption {
					return []grpc.DialOption{grpc.WithInsecure()}
				}},
			},
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
	}

	l := getTestLogger()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var wg sync.WaitGroup
			ctx, cancel := context.WithCancel(context.Background())
			nodes := []*Node{}
			for i, _ := range tc.cfg {
				wg.Add(1)
				n, err := MakeNode(ctx, &wg, tc.cfg[i],
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
			cancel()
			wg.Wait()
		})
	}
}

// Test mutual authentication between clients and servers. We set up three nodes, and a full mesh of mutually
// authenticated TLS client to server sesssion. We use self signed certificates. Simply replace (or omit) RootCAs
// for clients which are using certificates signed by a CA (if omited, OS configured CA would be used).
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
		Nodes: []string{":8080", ":8081", ":8082"},
		ClientDialOptionsFn: func(local, remote string) []grpc.DialOption {
			tlsCfg := &tls.Config{
				ServerName:   serverToName[remote],                   // server name,
				Certificates: []tls.Certificate{serverToCert[local]}, // client cert
				RootCAs:      caPool,
			}
			return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))}
		},
		ServerOptionsFn: func(local string) []grpc.ServerOption {
			tlsCfg := &tls.Config{
				ClientAuth:   tls.RequireAndVerifyClientCert,
				Certificates: []tls.Certificate{serverToCert[local]},
				ClientCAs:    caPool,
			}
			return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}
		},
	}

	l := getTestLogger()

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	nodes := []*Node{}
	for i := 0; i < len(nc.Nodes); i++ {
		wg.Add(1)
		n, err := MakeNode(ctx, &wg, nc, WithLogger(l.Named(fmt.Sprintf("LOG%d", i)), true))

		if err != nil {
			t.Fatalf("%s, expected ok, got [%v]", t.Name(), err)
		}
		nodes = append(nodes, n)
	}

	err = evalConnectedClients(ctx, nodes)
	if err != nil {
		t.Error(err)
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
		messaging: &raftMessaging{grpcLogging: true},
		logger:    l.Sugar(),
		config: &NodeConfig{Nodes: []string{
			"1.2.3.4:12345", // we expect this to not be picked because it is not local
			":8989",         // we expect this to be picked because it is local
		}}}
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
	cfg.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		return []grpc.DialOption{grpc.WithInsecure()}
	}

	wg.Add(1)
	n, err := MakeNode(ctx, &wg, cfg)
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
	cfg.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		return []grpc.DialOption{grpc.WithInsecure()}
	}

	loggerCfg := DefaultZapLoggerConfig()
	logger, err := loggerCfg.Build( /* custom options can be provided here */ )
	if err != nil {
		//...
	}

	wg.Add(1)
	n, err := MakeNode(ctx, &wg, cfg, WithLogger(logger, true))
	if err != nil {
		/// handle error
	}

	//
	// At any point, the logging level can be safely and concurrently, changed.
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
		Nodes: []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"},
		ClientDialOptionsFn: func(local, remote string) []grpc.DialOption {
			tlsCfg := &tls.Config{
				ServerName:   serverToName[remote],
				Certificates: []tls.Certificate{localCert},
				// If RootCAs is not set, host OS root CA set is used to validate server certificate.
				// Alternatively, custom Cert CA pool to use to validate server certificate would be set up here.
				RootCAs: certPool,
			}
			return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))}
		},
		ServerOptionsFn: func(local string) []grpc.ServerOption {
			tlsCfg := &tls.Config{
				ClientAuth:   tls.RequireAndVerifyClientCert,
				Certificates: []tls.Certificate{localCert},
				// If ClientCAs is not set, host OS root CA set is used to validate client certificate.
				// Alternatively, custom Cert CA pool to use to validate server certificate would be set up here.
				// ClientCAs pool does NOT need to be the same as RootCAs pool.
				ClientCAs: certPool,
			}
			return []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}
		},
	}

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	l, err := DefaultZapLoggerConfig().Build()
	if err != nil {
		// handle err
	}

	wg.Add(1)
	n, err := MakeNode(ctx, &wg, nc, WithLogger(l, true))
	if err != nil {
		// handle err
	}

	fmt.Printf("node started with config [%v]", n.config)

	cancel()
	wg.Wait()

}

func getTestLogger() *zap.Logger {
	cfg := DefaultZapLoggerConfig()
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

type testEvent struct {
	name  string
	count *atomic.Int32
	wg    *sync.WaitGroup
}

func (t *testEvent) handle() {
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
	// Number of events we expect for full mesh of connectivity between nodes: n-1.
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
					n.messaging.server.index, cl.index)
			}
		}
		eventsRxed := e.waitForHitsOrTimeout(int32(expect), time.Second)
		if !eventsRxed {
			return fmt.Errorf("At node %d, expected %d events, and got %d",
				n.messaging.server.index, expect, e.count.Load())
		}
	}
	return nil
}
