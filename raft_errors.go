package raft

import (
	werrors "github.com/pkg/errors"
)

// For the same reason as we centralise metrics, we also centralise errors: to make it
// as easy as possible to keep errors consistent.
//
// The approach to errors in the raft package is a nod to the solution to the 'semantics
// of error values' problem described in one of the go2 error proposals.
//
// https://go.googlesource.com/proposal/+/master/design/go2draft-error-values-overview.md
//
// Until go2 arrives, raft relies on pkg/errors to wrap errors from beneath without obscuring
// them.
//
// For errors originating within the raft package, we use raft sentinel errors (which can
// be processed programmatically) as cause; wrapped with message and context in order to
// satisfy the need to translate errors into logs upstream.
//
// For errors originating in downstream packages of raft, raft wraps the original error,
// with a raft-recognisable message. When processing the cause of an error returned by
// raft, a client can do one of a couple of ways...
//
// A. if simply logging a message, than the error can be treated like any other error.
// B. if wishing to test an error returned by raft package against a sentinel error,
//    simply call errors.Cause() on it and compare it to sentinel values.
//

// Keyword for error field in logger...
const raftErrKeyword = "err"
const raftSentinel = "errCode: "

// Error implements the error interface and represents sentinel errors for the raft package (as per https://dave.cheney.net/2016/04/07/constant-errors).
type Error string

func (e Error) Error() string { return string(e) }

// RaftErrorBadMakeNodeOption is returned (extracted using errors.Cause(err)) if options
// provided at start up fail to apply. See ExampleMakeNode for an example of how to extract
// and test against sentinel.
const RaftErrorBadMakeNodeOption = Error(raftSentinel + "bad MakeNode option")

// RaftErrorBadLocalNodeIndex is returned (extracted using errors.Cause(err)) if localNodeIndex
// provided is incorrect - typically out-of-bounds of Nodes in cluster. See ExampleMakeNode for an
// example of how to extract and test against sentinel.
const RaftErrorBadLocalNodeIndex = Error(raftSentinel + "bad localNodeIndex option")

// RaftErrorServerNotSetup is the sentinel returned  (extracted using errors.Cause(err)) if
// local address (server side) is not set up when expected. See ExampleMakeNode for an example of how to
// extract and test against sentinel.
const RaftErrorServerNotSetup = Error(raftSentinel + "local server side not set up yet")

// RaftErrorClientConnectionUnrecoverable is the sentinel returned  (extracted using errors.Cause(err)) if
// client gRPC connection to remote node failed. See ExampleMakeNode for an example of how to extract and test against
// sentinel.
const RaftErrorClientConnectionUnrecoverable = Error(
	raftSentinel + "gRPC client connection failed in an unrecoverable way. Check NodeConfig is correct.")

// RaftErrorMissingLogger is returned (extracted using errors.Cause(err)) if options
// provided at start up fail to apply. See ExampleMakeNode for an example of how to extract
// and test against sentinel.
const RaftErrorMissingLogger = Error(raftSentinel + "no logger setup")

// RaftErrorMissingNodeConfig is returned (extracted using errors.Cause(err)) if NodeConfig options
// provided at start are expected but missing. See ExampleMakeNode for an example of how to extract
// and test against sentinel.
const RaftErrorMissingNodeConfig = Error(raftSentinel + "node config insufficient")

// RaftErrorLeaderTransitionInTerm is returned (extracted using errors.Cause(err)) if a transition in leader
// happens without a change in term. This is a catastrophic unexpected error and would cause a shutdown
// of raft package if it occurred.
const RaftErrorLeaderTransitionInTerm = Error(raftSentinel + "mid term leader transition")

// RaftErrorOutOfBoundsClient is returned (extracted using errors.Cause(err)) if logic produces a client
// index for a client which does not exists. See ExampleMakeNode for an example of how to extract
// and test against sentinel.
const RaftErrorOutOfBoundsClient = Error(raftSentinel + "node index outside bounds of known clients")

// RaftErrorNodePersistentData is returned (extracted using errors.Cause(err)) if we fail a bolt operation on the
// persistent node data in BoltDB. See ExampleMakeNode for an example of how to extract and test against sentinel.
const RaftErrorNodePersistentData = Error(raftSentinel + "node persistent data failed")

// RaftErrorLogCommandRejected is returned (extracted using errors.Cause(err)) if we fail to commit a log command
// requested by the application. See ExampleMakeNode for an example of how to extract and test against sentinel.
const RaftErrorLogCommandRejected = Error(raftSentinel + "log command failed to commit")

// RaftErrorLogCommandLocalDrop is returned (extracted using errors.Cause(err)) if the local raft package drops
// the log command before we even try to push it to the cluster leader.
const RaftErrorLogCommandLocalDrop = Error(raftSentinel + "log command dropped locally, please retry")

// RaftErrorMustFailed is returned (extracted using errors.Cause(err)) if the local raft package hits an assertion
// failure. This will cause the raft package to signal a catastrophic failure and shut itself down
const RaftErrorMustFailed = Error(raftSentinel + "raft internal assertion, shutting down local node")

// raftErrorMismatchedTerm is returned (extract using errors.Cause(err)) within the raft package, and is used
// to signal that a mismatched term has been detected in message from remote node.
const raftErrorMismatchedTerm = Error(raftSentinel + "mismatched term")

// raftErrorf is a simple wrapper which ensures that all raft errors are prefixed
// consistently, and that we always either wrap a root cause error bubbling up from
// packages beneath raft, or a sentinel error from above.
func raftErrorf(rootCause error, format string, args ...interface{}) error {
	return werrors.WithMessagef(rootCause, "raft: "+format, args...)
}
