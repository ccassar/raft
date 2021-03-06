// Code generated by protoc-gen-go. DO NOT EDIT.
// source: raft.proto

package raft_pb

import (
	context "context"
	fmt "fmt"
	proto "github.com/golang/protobuf/proto"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	math "math"
)

// Reference imports to suppress errors if they are not otherwise used.
var _ = proto.Marshal
var _ = fmt.Errorf
var _ = math.Inf

// This is a compile-time assertion to ensure that this generated file
// is compatible with the proto package it is being compiled against.
// A compilation error at this line likely means your copy of the
// proto package needs to be updated.
const _ = proto.ProtoPackageIsVersion3 // please upgrade the proto package

// RequestVoteRequest are initiated by node in Candidate state, on election.
// Fields matched to condensed summary of Raft consensus algorithm in ISUCA.
type RequestVoteRequest struct {
	Term         int64 `protobuf:"varint,1,opt,name=term,proto3" json:"term,omitempty"`
	CandidateId  int32 `protobuf:"varint,2,opt,name=candidate_id,json=candidateId,proto3" json:"candidate_id,omitempty"`
	LastLogIndex int64 `protobuf:"varint,3,opt,name=last_log_index,json=lastLogIndex,proto3" json:"last_log_index,omitempty"`
	LastLogTerm  int64 `protobuf:"varint,4,opt,name=last_log_term,json=lastLogTerm,proto3" json:"last_log_term,omitempty"`
	// Index indicating voterId we are requesting from, for diagnostics.
	To                   int32    `protobuf:"varint,5,opt,name=To,proto3" json:"To,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *RequestVoteRequest) Reset()         { *m = RequestVoteRequest{} }
func (m *RequestVoteRequest) String() string { return proto.CompactTextString(m) }
func (*RequestVoteRequest) ProtoMessage()    {}
func (*RequestVoteRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{0}
}

func (m *RequestVoteRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_RequestVoteRequest.Unmarshal(m, b)
}
func (m *RequestVoteRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_RequestVoteRequest.Marshal(b, m, deterministic)
}
func (m *RequestVoteRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_RequestVoteRequest.Merge(m, src)
}
func (m *RequestVoteRequest) XXX_Size() int {
	return xxx_messageInfo_RequestVoteRequest.Size(m)
}
func (m *RequestVoteRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_RequestVoteRequest.DiscardUnknown(m)
}

var xxx_messageInfo_RequestVoteRequest proto.InternalMessageInfo

func (m *RequestVoteRequest) GetTerm() int64 {
	if m != nil {
		return m.Term
	}
	return 0
}

func (m *RequestVoteRequest) GetCandidateId() int32 {
	if m != nil {
		return m.CandidateId
	}
	return 0
}

func (m *RequestVoteRequest) GetLastLogIndex() int64 {
	if m != nil {
		return m.LastLogIndex
	}
	return 0
}

func (m *RequestVoteRequest) GetLastLogTerm() int64 {
	if m != nil {
		return m.LastLogTerm
	}
	return 0
}

func (m *RequestVoteRequest) GetTo() int32 {
	if m != nil {
		return m.To
	}
	return 0
}

type RequestVoteReply struct {
	Term                 int64    `protobuf:"varint,1,opt,name=term,proto3" json:"term,omitempty"`
	VoterId              int32    `protobuf:"varint,2,opt,name=voter_id,json=voterId,proto3" json:"voter_id,omitempty"`
	VoteGranted          bool     `protobuf:"varint,3,opt,name=vote_granted,json=voteGranted,proto3" json:"vote_granted,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *RequestVoteReply) Reset()         { *m = RequestVoteReply{} }
func (m *RequestVoteReply) String() string { return proto.CompactTextString(m) }
func (*RequestVoteReply) ProtoMessage()    {}
func (*RequestVoteReply) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{1}
}

func (m *RequestVoteReply) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_RequestVoteReply.Unmarshal(m, b)
}
func (m *RequestVoteReply) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_RequestVoteReply.Marshal(b, m, deterministic)
}
func (m *RequestVoteReply) XXX_Merge(src proto.Message) {
	xxx_messageInfo_RequestVoteReply.Merge(m, src)
}
func (m *RequestVoteReply) XXX_Size() int {
	return xxx_messageInfo_RequestVoteReply.Size(m)
}
func (m *RequestVoteReply) XXX_DiscardUnknown() {
	xxx_messageInfo_RequestVoteReply.DiscardUnknown(m)
}

var xxx_messageInfo_RequestVoteReply proto.InternalMessageInfo

func (m *RequestVoteReply) GetTerm() int64 {
	if m != nil {
		return m.Term
	}
	return 0
}

func (m *RequestVoteReply) GetVoterId() int32 {
	if m != nil {
		return m.VoterId
	}
	return 0
}

func (m *RequestVoteReply) GetVoteGranted() bool {
	if m != nil {
		return m.VoteGranted
	}
	return false
}

// AppendEntryRequest are initiated by Leader nodes to request replication of log.
// AppendEntryRequest with no log entries are sent by Leader as keepalives,
// to stave off elections, and to convey highest committed sequence. The content
// matches precisely the content specified in Figure 2 of ISUCA (see README.md).
type AppendEntryRequest struct {
	Term           int64       `protobuf:"varint,1,opt,name=term,proto3" json:"term,omitempty"`
	LeaderId       int32       `protobuf:"varint,2,opt,name=leader_id,json=leaderId,proto3" json:"leader_id,omitempty"`
	PrevLogIndex   int64       `protobuf:"varint,3,opt,name=prev_log_index,json=prevLogIndex,proto3" json:"prev_log_index,omitempty"`
	PrevLogTerm    int64       `protobuf:"varint,4,opt,name=prev_log_term,json=prevLogTerm,proto3" json:"prev_log_term,omitempty"`
	CommittedIndex int64       `protobuf:"varint,5,opt,name=committed_index,json=committedIndex,proto3" json:"committed_index,omitempty"`
	LogEntry       []*LogEntry `protobuf:"bytes,6,rep,name=log_entry,json=logEntry,proto3" json:"log_entry,omitempty"`
	// diagnostics only, to points at destination message is being sent to.
	To                   int32    `protobuf:"varint,7,opt,name=to,proto3" json:"to,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *AppendEntryRequest) Reset()         { *m = AppendEntryRequest{} }
func (m *AppendEntryRequest) String() string { return proto.CompactTextString(m) }
func (*AppendEntryRequest) ProtoMessage()    {}
func (*AppendEntryRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{2}
}

func (m *AppendEntryRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_AppendEntryRequest.Unmarshal(m, b)
}
func (m *AppendEntryRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_AppendEntryRequest.Marshal(b, m, deterministic)
}
func (m *AppendEntryRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_AppendEntryRequest.Merge(m, src)
}
func (m *AppendEntryRequest) XXX_Size() int {
	return xxx_messageInfo_AppendEntryRequest.Size(m)
}
func (m *AppendEntryRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_AppendEntryRequest.DiscardUnknown(m)
}

var xxx_messageInfo_AppendEntryRequest proto.InternalMessageInfo

func (m *AppendEntryRequest) GetTerm() int64 {
	if m != nil {
		return m.Term
	}
	return 0
}

func (m *AppendEntryRequest) GetLeaderId() int32 {
	if m != nil {
		return m.LeaderId
	}
	return 0
}

func (m *AppendEntryRequest) GetPrevLogIndex() int64 {
	if m != nil {
		return m.PrevLogIndex
	}
	return 0
}

func (m *AppendEntryRequest) GetPrevLogTerm() int64 {
	if m != nil {
		return m.PrevLogTerm
	}
	return 0
}

func (m *AppendEntryRequest) GetCommittedIndex() int64 {
	if m != nil {
		return m.CommittedIndex
	}
	return 0
}

func (m *AppendEntryRequest) GetLogEntry() []*LogEntry {
	if m != nil {
		return m.LogEntry
	}
	return nil
}

func (m *AppendEntryRequest) GetTo() int32 {
	if m != nil {
		return m.To
	}
	return 0
}

type AppendEntryReply struct {
	Term                 int64    `protobuf:"varint,1,opt,name=term,proto3" json:"term,omitempty"`
	Ack                  bool     `protobuf:"varint,2,opt,name=ack,proto3" json:"ack,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *AppendEntryReply) Reset()         { *m = AppendEntryReply{} }
func (m *AppendEntryReply) String() string { return proto.CompactTextString(m) }
func (*AppendEntryReply) ProtoMessage()    {}
func (*AppendEntryReply) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{3}
}

func (m *AppendEntryReply) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_AppendEntryReply.Unmarshal(m, b)
}
func (m *AppendEntryReply) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_AppendEntryReply.Marshal(b, m, deterministic)
}
func (m *AppendEntryReply) XXX_Merge(src proto.Message) {
	xxx_messageInfo_AppendEntryReply.Merge(m, src)
}
func (m *AppendEntryReply) XXX_Size() int {
	return xxx_messageInfo_AppendEntryReply.Size(m)
}
func (m *AppendEntryReply) XXX_DiscardUnknown() {
	xxx_messageInfo_AppendEntryReply.DiscardUnknown(m)
}

var xxx_messageInfo_AppendEntryReply proto.InternalMessageInfo

func (m *AppendEntryReply) GetTerm() int64 {
	if m != nil {
		return m.Term
	}
	return 0
}

func (m *AppendEntryReply) GetAck() bool {
	if m != nil {
		return m.Ack
	}
	return false
}

type RequestTimeoutRequest struct {
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *RequestTimeoutRequest) Reset()         { *m = RequestTimeoutRequest{} }
func (m *RequestTimeoutRequest) String() string { return proto.CompactTextString(m) }
func (*RequestTimeoutRequest) ProtoMessage()    {}
func (*RequestTimeoutRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{4}
}

func (m *RequestTimeoutRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_RequestTimeoutRequest.Unmarshal(m, b)
}
func (m *RequestTimeoutRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_RequestTimeoutRequest.Marshal(b, m, deterministic)
}
func (m *RequestTimeoutRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_RequestTimeoutRequest.Merge(m, src)
}
func (m *RequestTimeoutRequest) XXX_Size() int {
	return xxx_messageInfo_RequestTimeoutRequest.Size(m)
}
func (m *RequestTimeoutRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_RequestTimeoutRequest.DiscardUnknown(m)
}

var xxx_messageInfo_RequestTimeoutRequest proto.InternalMessageInfo

type RequestTimeoutReply struct {
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *RequestTimeoutReply) Reset()         { *m = RequestTimeoutReply{} }
func (m *RequestTimeoutReply) String() string { return proto.CompactTextString(m) }
func (*RequestTimeoutReply) ProtoMessage()    {}
func (*RequestTimeoutReply) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{5}
}

func (m *RequestTimeoutReply) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_RequestTimeoutReply.Unmarshal(m, b)
}
func (m *RequestTimeoutReply) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_RequestTimeoutReply.Marshal(b, m, deterministic)
}
func (m *RequestTimeoutReply) XXX_Merge(src proto.Message) {
	xxx_messageInfo_RequestTimeoutReply.Merge(m, src)
}
func (m *RequestTimeoutReply) XXX_Size() int {
	return xxx_messageInfo_RequestTimeoutReply.Size(m)
}
func (m *RequestTimeoutReply) XXX_DiscardUnknown() {
	xxx_messageInfo_RequestTimeoutReply.DiscardUnknown(m)
}

var xxx_messageInfo_RequestTimeoutReply proto.InternalMessageInfo

type LogCommandRequest struct {
	Command              []byte   `protobuf:"bytes,1,opt,name=command,proto3" json:"command,omitempty"`
	Origin               int32    `protobuf:"varint,2,opt,name=origin,proto3" json:"origin,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *LogCommandRequest) Reset()         { *m = LogCommandRequest{} }
func (m *LogCommandRequest) String() string { return proto.CompactTextString(m) }
func (*LogCommandRequest) ProtoMessage()    {}
func (*LogCommandRequest) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{6}
}

func (m *LogCommandRequest) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_LogCommandRequest.Unmarshal(m, b)
}
func (m *LogCommandRequest) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_LogCommandRequest.Marshal(b, m, deterministic)
}
func (m *LogCommandRequest) XXX_Merge(src proto.Message) {
	xxx_messageInfo_LogCommandRequest.Merge(m, src)
}
func (m *LogCommandRequest) XXX_Size() int {
	return xxx_messageInfo_LogCommandRequest.Size(m)
}
func (m *LogCommandRequest) XXX_DiscardUnknown() {
	xxx_messageInfo_LogCommandRequest.DiscardUnknown(m)
}

var xxx_messageInfo_LogCommandRequest proto.InternalMessageInfo

func (m *LogCommandRequest) GetCommand() []byte {
	if m != nil {
		return m.Command
	}
	return nil
}

func (m *LogCommandRequest) GetOrigin() int32 {
	if m != nil {
		return m.Origin
	}
	return 0
}

type LogCommandReply struct {
	Ack                  bool     `protobuf:"varint,1,opt,name=ack,proto3" json:"ack,omitempty"`
	Reason               string   `protobuf:"bytes,2,opt,name=reason,proto3" json:"reason,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *LogCommandReply) Reset()         { *m = LogCommandReply{} }
func (m *LogCommandReply) String() string { return proto.CompactTextString(m) }
func (*LogCommandReply) ProtoMessage()    {}
func (*LogCommandReply) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{7}
}

func (m *LogCommandReply) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_LogCommandReply.Unmarshal(m, b)
}
func (m *LogCommandReply) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_LogCommandReply.Marshal(b, m, deterministic)
}
func (m *LogCommandReply) XXX_Merge(src proto.Message) {
	xxx_messageInfo_LogCommandReply.Merge(m, src)
}
func (m *LogCommandReply) XXX_Size() int {
	return xxx_messageInfo_LogCommandReply.Size(m)
}
func (m *LogCommandReply) XXX_DiscardUnknown() {
	xxx_messageInfo_LogCommandReply.DiscardUnknown(m)
}

var xxx_messageInfo_LogCommandReply proto.InternalMessageInfo

func (m *LogCommandReply) GetAck() bool {
	if m != nil {
		return m.Ack
	}
	return false
}

func (m *LogCommandReply) GetReason() string {
	if m != nil {
		return m.Reason
	}
	return ""
}

// AppNonce, a test message used in unit test which can be safely ignored.
type AppNonce struct {
	Nonce                int64    `protobuf:"varint,1,opt,name=nonce,proto3" json:"nonce,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *AppNonce) Reset()         { *m = AppNonce{} }
func (m *AppNonce) String() string { return proto.CompactTextString(m) }
func (*AppNonce) ProtoMessage()    {}
func (*AppNonce) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{8}
}

func (m *AppNonce) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_AppNonce.Unmarshal(m, b)
}
func (m *AppNonce) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_AppNonce.Marshal(b, m, deterministic)
}
func (m *AppNonce) XXX_Merge(src proto.Message) {
	xxx_messageInfo_AppNonce.Merge(m, src)
}
func (m *AppNonce) XXX_Size() int {
	return xxx_messageInfo_AppNonce.Size(m)
}
func (m *AppNonce) XXX_DiscardUnknown() {
	xxx_messageInfo_AppNonce.DiscardUnknown(m)
}

var xxx_messageInfo_AppNonce proto.InternalMessageInfo

func (m *AppNonce) GetNonce() int64 {
	if m != nil {
		return m.Nonce
	}
	return 0
}

//
// Serialised persisted state for all nodes.
type PersistedState struct {
	VotedFor             int32    `protobuf:"varint,1,opt,name=voted_for,json=votedFor,proto3" json:"voted_for,omitempty"`
	CurrentTerm          int64    `protobuf:"varint,2,opt,name=current_term,json=currentTerm,proto3" json:"current_term,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *PersistedState) Reset()         { *m = PersistedState{} }
func (m *PersistedState) String() string { return proto.CompactTextString(m) }
func (*PersistedState) ProtoMessage()    {}
func (*PersistedState) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{9}
}

func (m *PersistedState) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_PersistedState.Unmarshal(m, b)
}
func (m *PersistedState) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_PersistedState.Marshal(b, m, deterministic)
}
func (m *PersistedState) XXX_Merge(src proto.Message) {
	xxx_messageInfo_PersistedState.Merge(m, src)
}
func (m *PersistedState) XXX_Size() int {
	return xxx_messageInfo_PersistedState.Size(m)
}
func (m *PersistedState) XXX_DiscardUnknown() {
	xxx_messageInfo_PersistedState.DiscardUnknown(m)
}

var xxx_messageInfo_PersistedState proto.InternalMessageInfo

func (m *PersistedState) GetVotedFor() int32 {
	if m != nil {
		return m.VotedFor
	}
	return 0
}

func (m *PersistedState) GetCurrentTerm() int64 {
	if m != nil {
		return m.CurrentTerm
	}
	return 0
}

// Serialised Log Entries
type LogEntry struct {
	Term                 int64    `protobuf:"varint,1,opt,name=term,proto3" json:"term,omitempty"`
	Sequence             int64    `protobuf:"varint,2,opt,name=sequence,proto3" json:"sequence,omitempty"`
	Data                 []byte   `protobuf:"bytes,3,opt,name=data,proto3" json:"data,omitempty"`
	XXX_NoUnkeyedLiteral struct{} `json:"-"`
	XXX_unrecognized     []byte   `json:"-"`
	XXX_sizecache        int32    `json:"-"`
}

func (m *LogEntry) Reset()         { *m = LogEntry{} }
func (m *LogEntry) String() string { return proto.CompactTextString(m) }
func (*LogEntry) ProtoMessage()    {}
func (*LogEntry) Descriptor() ([]byte, []int) {
	return fileDescriptor_b042552c306ae59b, []int{10}
}

func (m *LogEntry) XXX_Unmarshal(b []byte) error {
	return xxx_messageInfo_LogEntry.Unmarshal(m, b)
}
func (m *LogEntry) XXX_Marshal(b []byte, deterministic bool) ([]byte, error) {
	return xxx_messageInfo_LogEntry.Marshal(b, m, deterministic)
}
func (m *LogEntry) XXX_Merge(src proto.Message) {
	xxx_messageInfo_LogEntry.Merge(m, src)
}
func (m *LogEntry) XXX_Size() int {
	return xxx_messageInfo_LogEntry.Size(m)
}
func (m *LogEntry) XXX_DiscardUnknown() {
	xxx_messageInfo_LogEntry.DiscardUnknown(m)
}

var xxx_messageInfo_LogEntry proto.InternalMessageInfo

func (m *LogEntry) GetTerm() int64 {
	if m != nil {
		return m.Term
	}
	return 0
}

func (m *LogEntry) GetSequence() int64 {
	if m != nil {
		return m.Sequence
	}
	return 0
}

func (m *LogEntry) GetData() []byte {
	if m != nil {
		return m.Data
	}
	return nil
}

func init() {
	proto.RegisterType((*RequestVoteRequest)(nil), "raft_pb.RequestVoteRequest")
	proto.RegisterType((*RequestVoteReply)(nil), "raft_pb.RequestVoteReply")
	proto.RegisterType((*AppendEntryRequest)(nil), "raft_pb.AppendEntryRequest")
	proto.RegisterType((*AppendEntryReply)(nil), "raft_pb.AppendEntryReply")
	proto.RegisterType((*RequestTimeoutRequest)(nil), "raft_pb.RequestTimeoutRequest")
	proto.RegisterType((*RequestTimeoutReply)(nil), "raft_pb.RequestTimeoutReply")
	proto.RegisterType((*LogCommandRequest)(nil), "raft_pb.LogCommandRequest")
	proto.RegisterType((*LogCommandReply)(nil), "raft_pb.LogCommandReply")
	proto.RegisterType((*AppNonce)(nil), "raft_pb.AppNonce")
	proto.RegisterType((*PersistedState)(nil), "raft_pb.PersistedState")
	proto.RegisterType((*LogEntry)(nil), "raft_pb.LogEntry")
}

func init() { proto.RegisterFile("raft.proto", fileDescriptor_b042552c306ae59b) }

var fileDescriptor_b042552c306ae59b = []byte{
	// 610 bytes of a gzipped FileDescriptorProto
	0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0xff, 0x7c, 0x54, 0x51, 0x4f, 0xdb, 0x30,
	0x10, 0x56, 0x5b, 0x4a, 0xc3, 0xa5, 0x2b, 0x60, 0xc6, 0x16, 0xca, 0x34, 0x75, 0xd6, 0xa4, 0xf1,
	0xd4, 0x07, 0xf6, 0x32, 0x89, 0x27, 0x36, 0x75, 0x13, 0x52, 0x85, 0x50, 0xa8, 0xf6, 0x1a, 0x99,
	0xd8, 0x44, 0xd1, 0x92, 0xd8, 0x73, 0x0c, 0x1a, 0x3f, 0x68, 0xff, 0x6e, 0x3f, 0x60, 0x8f, 0xd3,
	0xd9, 0x6e, 0x48, 0x69, 0xd9, 0xdb, 0xdd, 0xe7, 0xcb, 0xdd, 0xe7, 0xef, 0x3b, 0x07, 0x40, 0xb3,
	0x5b, 0x33, 0x55, 0x5a, 0x1a, 0x49, 0x06, 0x18, 0x27, 0xea, 0x86, 0xfe, 0xee, 0x00, 0x89, 0xc5,
	0xcf, 0x3b, 0x51, 0x9b, 0xef, 0xd2, 0x08, 0x1f, 0x12, 0x02, 0x5b, 0x46, 0xe8, 0x32, 0xea, 0x4c,
	0x3a, 0x27, 0xbd, 0xd8, 0xc6, 0xe4, 0x1d, 0x0c, 0x53, 0x56, 0xf1, 0x9c, 0x33, 0x23, 0x92, 0x9c,
	0x47, 0xdd, 0x49, 0xe7, 0xa4, 0x1f, 0x87, 0x0d, 0x76, 0xc1, 0xc9, 0x7b, 0x18, 0x15, 0xac, 0x36,
	0x49, 0x21, 0xb3, 0x24, 0xaf, 0xb8, 0xf8, 0x15, 0xf5, 0x6c, 0x83, 0x21, 0xa2, 0x73, 0x99, 0x5d,
	0x20, 0x46, 0x28, 0xbc, 0x68, 0xaa, 0xec, 0x94, 0x2d, 0x5b, 0x14, 0xfa, 0xa2, 0x05, 0x0e, 0x1b,
	0x41, 0x77, 0x21, 0xa3, 0xbe, 0x1d, 0xd1, 0x5d, 0x48, 0xca, 0x61, 0x6f, 0x85, 0xa6, 0x2a, 0x1e,
	0x36, 0x92, 0x3c, 0x82, 0xe0, 0x5e, 0x1a, 0xa1, 0x1f, 0x09, 0x0e, 0x6c, 0x7e, 0xc1, 0x91, 0x3f,
	0x86, 0x49, 0xa6, 0x59, 0x65, 0x04, 0xb7, 0xd4, 0x82, 0x38, 0x44, 0xec, 0x9b, 0x83, 0xe8, 0xdf,
	0x0e, 0x90, 0x73, 0xa5, 0x44, 0xc5, 0x67, 0x95, 0xd1, 0x0f, 0xff, 0x53, 0xe3, 0x18, 0x76, 0x0a,
	0xc1, 0x78, 0x7b, 0x52, 0xe0, 0x00, 0xa7, 0x83, 0xd2, 0xe2, 0x7e, 0x5d, 0x07, 0x44, 0xdb, 0x3a,
	0x34, 0x55, 0x6d, 0x1d, 0x7c, 0x91, 0xd5, 0xe1, 0x03, 0xec, 0xa6, 0xb2, 0x2c, 0x73, 0x63, 0x04,
	0xf7, 0xad, 0xfa, 0xb6, 0x6a, 0xd4, 0xc0, 0xae, 0xd9, 0x14, 0x76, 0xb0, 0x8f, 0x40, 0xde, 0xd1,
	0xf6, 0xa4, 0x77, 0x12, 0x9e, 0xee, 0x4f, 0xbd, 0xcb, 0xd3, 0xb9, 0xcc, 0xdc, 0x85, 0x82, 0xc2,
	0x47, 0x28, 0xb0, 0x91, 0xd1, 0xc0, 0x09, 0x6c, 0x24, 0xfd, 0x04, 0x7b, 0x2b, 0x37, 0x7f, 0x4e,
	0xe0, 0x3d, 0xe8, 0xb1, 0xf4, 0x87, 0xbd, 0x71, 0x10, 0x63, 0x48, 0x5f, 0xc3, 0xa1, 0x17, 0x6a,
	0x91, 0x97, 0x42, 0xde, 0x19, 0x9f, 0xd1, 0x43, 0x38, 0x78, 0x7a, 0xa0, 0x8a, 0x07, 0x3a, 0x83,
	0xfd, 0xb9, 0xcc, 0xbe, 0xc8, 0xb2, 0x64, 0x15, 0x5f, 0x4a, 0x1c, 0xc1, 0x20, 0x75, 0x88, 0x9d,
	0x36, 0x8c, 0x97, 0x29, 0x79, 0x05, 0xdb, 0x52, 0xe7, 0x59, 0x5e, 0x79, 0x95, 0x7d, 0x46, 0xcf,
	0x60, 0xb7, 0xdd, 0x06, 0xf9, 0x7a, 0x6e, 0x9d, 0x86, 0x1b, 0x7e, 0xac, 0x05, 0xab, 0xa5, 0xfb,
	0x78, 0x27, 0xf6, 0x19, 0x9d, 0x40, 0x70, 0xae, 0xd4, 0xa5, 0xac, 0x52, 0x41, 0x5e, 0x42, 0xbf,
	0xc2, 0xc0, 0x5f, 0xd3, 0x25, 0xf4, 0x0a, 0x46, 0x57, 0x42, 0xd7, 0x79, 0x6d, 0x04, 0xbf, 0x36,
	0xcc, 0x08, 0x74, 0x1c, 0x77, 0x85, 0x27, 0xb7, 0x52, 0xdb, 0xda, 0x7e, 0x6c, 0x77, 0x8d, 0x7f,
	0x95, 0xda, 0x3e, 0x8e, 0x3b, 0xad, 0x45, 0x65, 0x9c, 0x95, 0x5d, 0x67, 0xa5, 0xc7, 0xd0, 0x4a,
	0x7a, 0x09, 0xc1, 0xd2, 0x87, 0x8d, 0xca, 0x8e, 0x21, 0xa8, 0x51, 0x0d, 0xa4, 0xe2, 0x3e, 0x6f,
	0x72, 0xac, 0xe7, 0xcc, 0x30, 0xbb, 0x46, 0xc3, 0xd8, 0xc6, 0xa7, 0x7f, 0xba, 0x10, 0xc6, 0xec,
	0xd6, 0x5c, 0x0b, 0x7d, 0x9f, 0xa7, 0x82, 0xcc, 0x20, 0x6c, 0x39, 0x48, 0x8e, 0x1b, 0xf7, 0xd7,
	0x37, 0x7a, 0x7c, 0xb4, 0xf9, 0x10, 0x45, 0x9c, 0x41, 0xd8, 0x7a, 0x69, 0xad, 0x36, 0xeb, 0xbf,
	0x89, 0x56, 0x9b, 0xb5, 0xc7, 0x79, 0x09, 0xa3, 0x55, 0xf3, 0xc9, 0xdb, 0xa7, 0xc5, 0xab, 0xeb,
	0x32, 0x7e, 0xf3, 0xec, 0x39, 0xf6, 0xfb, 0x0c, 0xf0, 0x68, 0x37, 0x19, 0xb7, 0x57, 0x7b, 0x75,
	0x95, 0xc6, 0xd1, 0xc6, 0x33, 0xec, 0x71, 0x06, 0x07, 0xe7, 0x4a, 0x15, 0x79, 0xca, 0x4c, 0x2e,
	0xab, 0xb9, 0x94, 0xea, 0x06, 0x97, 0x64, 0xbf, 0x2d, 0x86, 0xdd, 0x89, 0xf1, 0x3a, 0x74, 0xb3,
	0x6d, 0xff, 0x9c, 0x1f, 0xff, 0x05, 0x00, 0x00, 0xff, 0xff, 0x24, 0x80, 0x81, 0x0d, 0x47, 0x05,
	0x00, 0x00,
}

// Reference imports to suppress errors if they are not otherwise used.
var _ context.Context
var _ grpc.ClientConn

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
const _ = grpc.SupportPackageIsVersion4

// RaftServiceClient is the client API for RaftService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://godoc.org/google.golang.org/grpc#ClientConn.NewStream.
type RaftServiceClient interface {
	AppendEntry(ctx context.Context, in *AppendEntryRequest, opts ...grpc.CallOption) (*AppendEntryReply, error)
	RequestVote(ctx context.Context, in *RequestVoteRequest, opts ...grpc.CallOption) (*RequestVoteReply, error)
	RequestTimeout(ctx context.Context, in *RequestTimeoutRequest, opts ...grpc.CallOption) (*RequestTimeoutReply, error)
	LogCommand(ctx context.Context, in *LogCommandRequest, opts ...grpc.CallOption) (*LogCommandReply, error)
	ApplicationLoopback(ctx context.Context, in *AppNonce, opts ...grpc.CallOption) (*AppNonce, error)
}

type raftServiceClient struct {
	cc *grpc.ClientConn
}

func NewRaftServiceClient(cc *grpc.ClientConn) RaftServiceClient {
	return &raftServiceClient{cc}
}

func (c *raftServiceClient) AppendEntry(ctx context.Context, in *AppendEntryRequest, opts ...grpc.CallOption) (*AppendEntryReply, error) {
	out := new(AppendEntryReply)
	err := c.cc.Invoke(ctx, "/raft_pb.RaftService/AppendEntry", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *raftServiceClient) RequestVote(ctx context.Context, in *RequestVoteRequest, opts ...grpc.CallOption) (*RequestVoteReply, error) {
	out := new(RequestVoteReply)
	err := c.cc.Invoke(ctx, "/raft_pb.RaftService/RequestVote", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *raftServiceClient) RequestTimeout(ctx context.Context, in *RequestTimeoutRequest, opts ...grpc.CallOption) (*RequestTimeoutReply, error) {
	out := new(RequestTimeoutReply)
	err := c.cc.Invoke(ctx, "/raft_pb.RaftService/RequestTimeout", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *raftServiceClient) LogCommand(ctx context.Context, in *LogCommandRequest, opts ...grpc.CallOption) (*LogCommandReply, error) {
	out := new(LogCommandReply)
	err := c.cc.Invoke(ctx, "/raft_pb.RaftService/LogCommand", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *raftServiceClient) ApplicationLoopback(ctx context.Context, in *AppNonce, opts ...grpc.CallOption) (*AppNonce, error) {
	out := new(AppNonce)
	err := c.cc.Invoke(ctx, "/raft_pb.RaftService/ApplicationLoopback", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RaftServiceServer is the server API for RaftService service.
type RaftServiceServer interface {
	AppendEntry(context.Context, *AppendEntryRequest) (*AppendEntryReply, error)
	RequestVote(context.Context, *RequestVoteRequest) (*RequestVoteReply, error)
	RequestTimeout(context.Context, *RequestTimeoutRequest) (*RequestTimeoutReply, error)
	LogCommand(context.Context, *LogCommandRequest) (*LogCommandReply, error)
	ApplicationLoopback(context.Context, *AppNonce) (*AppNonce, error)
}

// UnimplementedRaftServiceServer can be embedded to have forward compatible implementations.
type UnimplementedRaftServiceServer struct {
}

func (*UnimplementedRaftServiceServer) AppendEntry(ctx context.Context, req *AppendEntryRequest) (*AppendEntryReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method AppendEntry not implemented")
}
func (*UnimplementedRaftServiceServer) RequestVote(ctx context.Context, req *RequestVoteRequest) (*RequestVoteReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RequestVote not implemented")
}
func (*UnimplementedRaftServiceServer) RequestTimeout(ctx context.Context, req *RequestTimeoutRequest) (*RequestTimeoutReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RequestTimeout not implemented")
}
func (*UnimplementedRaftServiceServer) LogCommand(ctx context.Context, req *LogCommandRequest) (*LogCommandReply, error) {
	return nil, status.Errorf(codes.Unimplemented, "method LogCommand not implemented")
}
func (*UnimplementedRaftServiceServer) ApplicationLoopback(ctx context.Context, req *AppNonce) (*AppNonce, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ApplicationLoopback not implemented")
}

func RegisterRaftServiceServer(s *grpc.Server, srv RaftServiceServer) {
	s.RegisterService(&_RaftService_serviceDesc, srv)
}

func _RaftService_AppendEntry_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(AppendEntryRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(RaftServiceServer).AppendEntry(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/raft_pb.RaftService/AppendEntry",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(RaftServiceServer).AppendEntry(ctx, req.(*AppendEntryRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _RaftService_RequestVote_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RequestVoteRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(RaftServiceServer).RequestVote(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/raft_pb.RaftService/RequestVote",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(RaftServiceServer).RequestVote(ctx, req.(*RequestVoteRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _RaftService_RequestTimeout_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RequestTimeoutRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(RaftServiceServer).RequestTimeout(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/raft_pb.RaftService/RequestTimeout",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(RaftServiceServer).RequestTimeout(ctx, req.(*RequestTimeoutRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _RaftService_LogCommand_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(LogCommandRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(RaftServiceServer).LogCommand(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/raft_pb.RaftService/LogCommand",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(RaftServiceServer).LogCommand(ctx, req.(*LogCommandRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _RaftService_ApplicationLoopback_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(AppNonce)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(RaftServiceServer).ApplicationLoopback(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/raft_pb.RaftService/ApplicationLoopback",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(RaftServiceServer).ApplicationLoopback(ctx, req.(*AppNonce))
	}
	return interceptor(ctx, in, info, handler)
}

var _RaftService_serviceDesc = grpc.ServiceDesc{
	ServiceName: "raft_pb.RaftService",
	HandlerType: (*RaftServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "AppendEntry",
			Handler:    _RaftService_AppendEntry_Handler,
		},
		{
			MethodName: "RequestVote",
			Handler:    _RaftService_RequestVote_Handler,
		},
		{
			MethodName: "RequestTimeout",
			Handler:    _RaftService_RequestTimeout_Handler,
		},
		{
			MethodName: "LogCommand",
			Handler:    _RaftService_LogCommand_Handler,
		},
		{
			MethodName: "ApplicationLoopback",
			Handler:    _RaftService_ApplicationLoopback_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "raft.proto",
}
