[![GoDoc](https://godoc.org/github.com/ccassar/raft?status.svg)](https://godoc.org/github.com/ccassar/raft)
[![Build Status](https://travis-ci.org/ccassar/raft.svg?branch=master)](https://travis-ci.org/ccassar/raft)
[![codecov](https://codecov.io/gh/ccassar/raft/branch/master/graph/badge.svg)](https://codecov.io/gh/ccassar/raft)
[![Go Report Card](https://goreportcard.com/badge/github.com/ccassar/raft)](https://goreportcard.com/report/github.com/ccassar/raft)

# raft

Yet another implementation of raft, in go.

This package is intended to be embeddable in any application run as a cluster of coordinating instances and 
wishing to benefit from a distributed replicated log.

The focus of this implementation is an all-batteries-included, production quality, complete implementation.
Intra-cluster connectivity is implemented over gRPC with support for TLS protection with mutual authentication.

Observability is central to a production-quality implementation; structured logging (using Uber zap library)
and metrics export (using prometheus library and gRPC interceptors for both metrics and logging) are an integral
part of the implementation.

Unit test coverage is high and it is a goal to keep it so; unit test code itself is a key component of the
implementation.


### Raft, The Consensus Algorithm

Key references for the implementation are:

0. Diego Ongaro and John Ousterhout, [In Search of an Understandable Consensus Algorithm. 2014](https://www.usenix.org/conference/atc14/technical-sessions/presentation/ongaro) (ISUCA)

1. Diego Ongaro. Consensus: [Bridging Theory and Practice. Stanford University Ph.D. Dissertation. Aug. 2014.](https://ongardie.net/var/blurbs/pubs/dissertation.pdf) (CBTP)

2. Heidi Howard. [ARC: analysis of Raft consensus. University of Cambridge, Computer Laboratory, Jul. 2014.](https://www.cl.cam.ac.uk/techreports/UCAM-CL-TR-857.pdf) (ARC)

Reference to the above in code uses acronyms included above (i.e. CBTP, ARC, ISUCA).

For an excellent short presentation about Raft, see: https://www.usenix.org/node/184041


Key assertions:

- a raft node can be in one of three states: Follower, Candidate and Leader.
- log entries only ever flow from leader to followers.
- a term will only ever have one leader.
- followers never accept log entries from leaders on smaller term.
- leaders never remove entries, and followers only remove entries which conflict with leader (and are by definition uncommitted).
- voters only vote for leader if leader is as up-to-date as voter at least.

Raft is not Byzantine fault tolerant. A Raft variant called [Tangaroa](http://www.scs.stanford.edu/14au-cs244b/labs/projects/copeland_zhong.pdf) proposes extensions
to make it so.

Core Raft provides clients with at-least-once guarantees (Section 6.3 CBTP). This is because a leader may
fail once a proposal has been committed but before an acknowledgment is sent to the client. The same section
proposes a method to support exactly-once guarantees for a proposal even in the context of concurrent writes
from the same session.


### Intracluster Messaging

Intracluster messaging in this package relies on gRPC. The gRPC server and client options can be configured by the
application. By default, the client and server are configured with aggressive maximum concurrent streams per transport,
and keepalive parameters and enforcement policy.

Both the server side and client side are set up with prometheus and zap logging interceptors in order to
provide consistent logging and metrics collection for gRPC calls. Note that RPC logging filters out anything other
than request vote RPCs to avoid the cost on the more common AppendEntry messages. A `detailed` configuration option
[WithMetrics](https://godoc.org/github.com/ccassar/raft#WithLogger) under the control of the application determines 
whether we track the latency distribution of RPC calls.

#### TLS: Protecting Intracluster Messaging

The Raft package supports protecting intra-cluster traffic with mutually authenticated TLS. Client dial
options can be provided by the application as part of `MakeNode()` initialisation. These `grpc.DialOptions` are
used by the local node when dialing into remote nodes. Godoc provides [an example](https://godoc.org/github.com/ccassar/raft#example-MakeNode--WithTLSConfiguration)
of how to run with TLS enabled and with mutual authentication between server and client.

### Metrics

The Raft package accepts a prometheus metrics registry and uses that to track key statistics of the Raft
 operation, including RPC metrics client and server side (using gRPC middleware interceptors).

### Logging

By default, the raft package will set up a customised production zap log: logging Info level and above,
structured and JSON formatted, with sampling and caller disabled, and stacktrace enabled for errors. The logger
field is set (using logger.Named()) to unambiguously indicate that logs are coming from raft package. Logs, by
default, look like this:

```
{"level":"info","ts":"2019-06-08T16:04:34.891+0100","logger":"raft","msg":"raft package, starting up (logging can be customised or disabled using WithLogger option)"}
{"level":"info","ts":"2019-06-08T16:04:34.892+0100","logger":"raft","msg":"listener acquired local node address","obj":"localNode","nodeIndex":0,"address":":8088"}
{"level":"info","ts":"2019-06-08T16:04:34.892+0100","logger":"raft","msg":"added remote node from configuration","obj":"remoteNode","nodeIndex":1,"address":":8089"}
{"level":"info","ts":"2019-06-08T16:04:34.892+0100","logger":"raft","msg":"added remote node from configuration","obj":"remoteNode","nodeIndex":2,"address":":8090"}
```

Raft logging is customisable in the MakeNode call, application controls logger through the WithLogging option.
See godoc [example for details](https://godoc.org/github.com/ccassar/raft#example-MakeNode--WithCustomisedLogLevel). WithLogger option allows app to disable logging, provide its
own zap logger, or, in conjunction with DefaultZapLoggerConfig, to start with the default logging configuration
in raft package, modify it based on application need, and activate it. 

### Error Handling

Errors returned from the raft package across APIs attempt to provide the necessary context to
clarify the circumstances leading to the error (in the usual form of a string returned via Error()
method on error). The same error can also be squeezed for a root cause using the errors.Cause()
method. If the root cause is internal to raft, then the sentinel error will be one of those defined
[here](raft_errors.go) and can be tested programmatically. If the root cause originated in some package
downstream of raft, then the downstream error is propagated explicitly. Godoc documentation includes an
[example](https://godoc.org/github.com/ccassar/raft#example-MakeNode) of how the errors can be handled 
programmatically. In essence:

```
	n, err := MakeNode(ctx, &wg, cfg, localNodeIndex, WithLogger(mylog))
	if err != nil {

		switch errors.Cause(err) {
		case RaftErrorBadMakeNodeOption:
			//
			// Handle specific sentinel in whichever way we see fit.
			// ...
		default:
			// Root cause is not a handled sentinel.
		}
		// err itself renders the full context not just the sentinel.
		fmt.Println(err)
	}

```


### Report Card

Lots to go, but do come inside and have a look.

Done so far:

 - General package infra: gRPC client and server setup, logging, metrics, UT

In progress;

- election state machine.

Target is to cover all of Raft including cluster membership extensions, log compaction, exactly-once 
guarantees to clients and, beyond Raft, to bring Byzantine fault tolerance via Tangaroa.


### Dependencies

Other than the usual dependencies (i.e. go installation), protoc needs to be installed as [described here](https://github.com/golang/protobuf) 
and the directory hosting protoc should be in the PATH. Running `go generate` will automatically regenerate 
generated source.


### Raw Design Notes

ASIDE; the choice of term 'client' can lead to confusion - almost invariably client is
referring to the gRPC client functionality which a Node uses to interact with other nodes'
servers in the cluster. The term 'application' is used to refer to the entity reading and consuming
log updates. In the raft specifications, 'application' is called client.
 
From raftEngine to gRPC client goroutines, we never block with out timeout. This constraint should always be satisfied
because this is what ensures that we never livelock with client goroutine pushing to raftEngine, and raftEngine
trying to push to raftEngine. 

#### Concurrency and Synchronisation

The package uses multiple goroutines;
 - a group of go routines offload communication to other cluster nodes (local node acting as a gRPC client to each
 of the other nodes in the cluster). A goroutine fed through a buffered channel receives messages which need to be
 communicated to the remote node. The goroutine handles the call and blocks waiting for response, and on receipt,
 delivers the response back to the raft engine thread.
 - the central goroutine handles the raft state machine. Messages received from other goroutines and timer events are
 the main inputs to the state machine.
 - An application facing goroutine is responsible for feeding the channel of 'applied' log entries to the application.
  
Synchronisation is lock free and largely message passing based. Other synchronisation primitives used include atomic
updates to track when channels between raft engine and gRPC client goroutines should be flushed. 
 
