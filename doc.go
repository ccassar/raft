/*

Package raft is yet another implementation of raft, in go.

This raft package is intended to be embeddable in any application run as a cluster of coordinating instances and
wishing to benefit from a distributed replicated log.

For an overview of the package, and the current state of the implementation, see: https://github.com/ccassar/raft/blob/master/README.md

Consuming Distributed Log

The application receives distributed log commands over a channel it provides at initialisation time. This channel is
used by the raft package to publish committed log commands. The channel can be consumed at the convenience of the
application and does not effect the operation of the raft protocol. This is made possible through a dedicated goroutine
whose responsibility is to synchronise the application to the tail of the distributed log by fetching any pending log
commands if channel has capacity. The application provides the channel. A buffered channel would ensure that the producer
does not underrun the application unnecessarily when the application is not synchronised.

Publishing To Distributed Log

Any application instance in the application cluster can publish to the distributed log. The `Node` function `LogProduce`
provides this facility. The API is blocking and will return no error on commit, or an error for any other outcome. Errors
include wrapped sentinel errors for programmatic handling both for follower and leader side errors. Note that errors
reflect Raft propagation errors not state machine errors once log command is applied - the state machine is completely
invisible to the raft package - the package only ensure a consistent distributed log of commands; each of which can
be applied independently and consistently (for success or failure) across all instances of the application cluster.


Embedding Raft Package

The code to embed and initialise a local raft instance is straightforward. Numerous examples are included. The common
pattern across examples goes like this:

MakeNode() is called to fire up the local node. MakeNode will setup the local node to communicate with the rest of the
remote nodes in the cluster. MakeNode takes a mandatory configuration block in the form of NodeConfig. All the fields in
NodeConfig must be set.

NodeConfig primarily dictates the composition of the remote cluster, the location of the boltdb file where logs and
metadata are persisted, and the channel to use to communicate committed log commands to the application.

MakeNode also takes a series of options; these options control, for example, whether and how to protect intra-cluster
gRPC communication, logging and metrics collection.

The code to run the local node (without TLS protection) for a three node cluster with default logging, and metrics
registered against the default prometheus registry would look like this:

 var wg sync.WaitGroup
 ctx, cancel := context.WithCancel(context.Background())

 cfg := NodeConfig{
 	Nodes: []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"},
 	LogDB: "/data/myboltdbfile",
 	LogCmds: make(chan []byte, 32),
 }

 // index 2 suggests we are running node3.example.com.
 node, err := MakeNode(ctx, &wg, cfg, 2, WithMetrics(nil, true))
 if err != nil {
	// Handle unrecoverable error
 }

 //
 // At this point we're all set up. We can go about our business and start to receive committed distributed log
 // commands irrespective of which node generates them (i.e. local or remote). We also want to learn about and handle
 // any underlying unrecoverable failures. (This probability of such errors is expected to be vanishingly small).
 raftUnrecoverableError := node.FatalErrorChannel()
 for {
	 select {
		case logCmd := <- cfg.LogCmds:
		// Handle committed distributed log commands
		case err := <- raftUnrecoverableError:
		// Raft took some underlying error. Handle as appropriate (fail to orchestrator, restart, etc).
		...
	 }
 }

 // We produce log commands to the distributed log using the LogProduce API.
 ctxWithTimeout, cancelMsg := context.WithTimeout(ctx, logCmdTimeout)
 err = node.LogProduce(ctxWithTimeout, msg)
 if err != nil {
	// Message was refused. Retry (e.g. using a backoff package)
 }
 cancelMsg()

 //
 // When we are done with the local node, we can shut it down, and wait until it cleans up. This commitIndex
 // can be followed irrespective of whether raft returned an error from MakeNode, asynchronously via the fatal
 // error channel, or no errors at all.
 cancel()
 wg.Wait()


Slightly more is required when initialising in MakeNode in order to set up TLS with mutual authentication. It is of course
possible to set up variations in between; e.g. where client verifies server certificate, but server does not validate client,
or even, skip certificate verification on both sides. The changes involve setting up a function to return the gRPC server
options on request, and similarly for the client options. Note that in the client options callback, we return a remote
server name for dial options of a connection from a client to a server. This server name would be expected to match
Common Name in X509 certificate. (Note how in the example we are mapping from the cluster node name we specify in the
Nodes configuration to the common name in case they are different).

 clientDialOptionsFn = func(local, remote string) []grpc.DialOption {
		tlsCfg := &tls.Config{
			ServerName:   serverToName[remote],
			Certificates: []tls.Certificate{localCert},
			// If RootCAs is not set, host OS root CA set is used to validate server certificate.
			// Alternatively, custom Cert CA pool to use to validate server certificate would be set up here.
			RootCAs: certPool,
		}
		return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))}
	}
 serverOptionsFn = func(local string) []grpc.ServerOption {
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

These options would be passed in using WithClientDialOptionsFn and WithServerOptionsFn respectively.

*/
package raft
