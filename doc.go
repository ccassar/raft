/*

Package raft is yet another implementation of raft, in go.

This raft package is intended to be embeddable in any application run as a cluster of coordinating instances and
wishing to benefit from a distributed replicated log.

For an overview of the package, and the current state of the implementation, see: https://github.com/ccassar/raft/blob/master/README.md

Embedding Raft Package, Initialisation

The code to embed and initialise a local raft instance is straightforward. Numerous examples are included, but right
here is the common pattern of the detailed variations presented in the examples.

The key function called is MakeNode. MakeNode will setup the local node to communicate with the rest of the remote
nodes in the cluster. MakeNode takes a configuration block in the form of NodeConfig. NodeConfig primarily dictates
the composition of the remote cluster, and gRPC server and client options including, for example, whether and how
to protect intracluster gRPC communication. MakeNode also takes a series of options; these options control logging
and metrics collection.

The code to run the local node without TLS protection for a three node cluster with default logging, and metrics
registered against the default prometheus registry would look like this:


 var wg sync.WaitGroup
 ctx, cancel := context.WithCancel(context.Background())

 cfg := NewNodeConfig()
 cfg.Nodes = []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"}
 // Explicitly request insecure intra-cluster communication. It is not provided as default.
 cfg.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption { return []grpc.DialOption{grpc.WithInsecure()} }

 // 2 suggests we are running node3.example.com.
 node, err := MakeNode(ctx, &wg, cfg, 2, WithMetrics(nil, true))
 if err != nil {
	// Handle unrecoverable error
 }

 //
 // At this point we're all set up. We can go about our business. We also want to learn about and handle any
 // underlying unrecoverable failures. (This probability of such errors is expected to be vanishingly small).
 raftUnrecoverableError := node.FatalErrorChannel()
 select {
	// ...
	case err := <- raftUnrecoverableError:
	  // Raft took some underlying error. Handle as appropriate (fail to orchestrator, restart, etc).
 }

 //
 // When we are done with the local node, we can shut it down, and wait until it cleans up. This commitIndex
 // can be followed irrespective of whether raft returned an error from MakeNode, asynchronously via the fatal
 // error channel, or no errors at all.
 cancel()
 wg.Wait()


Slightly more complicated setup is involved in order to set up TLS with mutual authentication. It is of course possible
to set up variations in between; e.g. where client verifies server cert, but server does not validate client, or even,
skip certificate verification on both sides. The key changes involve setting up a function to return the gRPC server
options on request, and similarly for the client options. Note that in the client options callback, we return a remote
server name for dial options of a connection from a client to a server. This server name would be expected to match
Common Name in X509 certificate. (Note how in the example we are mapping from the cluster node name we specify in the
Nodes configuration to the common name in case they are different).

 cfg.ClientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		tlsCfg := &tls.Config{
			ServerName:   serverToName[remote],
			Certificates: []tls.Certificate{localCert},
			// If RootCAs is not set, host OS root CA set is used to validate server certificate.
			// Alternatively, custom Cert CA pool to use to validate server certificate would be set up here.
			RootCAs: certPool,
		}
		return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))}
	}
 cfg.ServerOptionsFn = func(local string) []grpc.ServerOption {
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



*/
package raft
