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
const rAFT_ERR_KEYWORD = "err"
const rAFT_SENTINEL = "errCode: "

// Raft sentinel errors (as per https://dave.cheney.net/2016/04/07/constant-errors).
type Error string

func (e Error) Error() string { return string(e) }

// RaftErrorBadMakeNodeOption is returned (extracted using errors.Cause(err)) if options
// provided at start up fail to apply. See ExampleMakeNode for an example of how to extract
// and test against sentinel.
const RaftErrorBadMakeNodeOption = Error(rAFT_SENTINEL + "bad MakeNode option")

// RaftErrorServerNotSetup is the sentinel returned  (extracted using errors.Cause(err)) if
// local address (server side) is not set up when expected. See ExampleMakeNode for an example of how to
// extract and test against sentinel.
const RaftErrorServerNotSetup = Error(rAFT_SENTINEL + "local server side not set up yet")

// RaftErrorClientConnectionUnrecoverable is the sentinel returned  (extracted using errors.Cause(err)) if
// client gRPC connection to remote node failed. See ExampleMakeNode for an example of how to extract and test against
// sentinel.
const RaftErrorClientConnectionUnrecoverable = Error(
	rAFT_SENTINEL + "gRPC client connection failed in an unrecoverable way. Check NodeConfig is correct.")

// RaftErrorMissingLogger is returned (extracted using errors.Cause(err)) if options
// provided at start up fail to apply. See ExampleMakeNode for an example of how to extract
// and test against sentinel.
const RaftErrorMissingLogger = Error(rAFT_SENTINEL + "no logger setup")

// RaftErrorMissingNodeConfig is returned (extracted using errors.Cause(err)) if config options
// provided at start are expected but missing. See ExampleMakeNode for an example of how to extract
// and test against sentinel.
const RaftErrorMissingNodeConfig = Error(rAFT_SENTINEL + "node config insufficient")

// raftErrorf is a simple wrapper which ensures that all raft errors are prefixed
// consistently, and that we always either wrap a root cause error bubbling up from
// packages beneath raft, or a sentinel error from above.
func raftErrorf(rootCause error, format string, args ...interface{}) error {
	return werrors.WithMessagef(rootCause, "raft: "+format, args...)
}
