package raft

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"github.com/ccassar/raft/internal/raft_pb"
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"
	bolt "go.etcd.io/bbolt"
	"time"
)

// logEntryGetSerialisedKey returns the []byte for key of log entry. We want this byte slice to provide ordering
// of the log (so we can use cursors to iterate over log entries in index order). Protobuf encoding for int64
// (varint) does not produce byte stream with lexical order which matches the value of the int64 so we do not
// use the protobuf encoding here.
func logEntryGetSerialisedKey(le *raft_pb.LogEntry) []byte {
	var key bytes.Buffer
	binary.Write(&key, binary.BigEndian, le.Sequence)
	return key.Bytes()
}

func sequenceFromSerialisedKey(b []byte) (int64, error) {
	var sequence int64
	buf := bytes.NewBuffer(b)
	err := binary.Read(buf, binary.BigEndian, &sequence)
	return sequence, err
}

func logEntryGetSerialised(le *raft_pb.LogEntry) ([]byte, error) {
	return proto.Marshal(le)
}

func logEntryFromSerialised(b []byte) (*raft_pb.LogEntry, error) {

	var l raft_pb.LogEntry
	err := proto.Unmarshal(b, &l)

	return &l, err
}

func (re *raftEngine) logAddEntry(le *raft_pb.LogEntry) error {

	var err error
	defer func() {
		if err != nil {
			err = raftErrorf(err, "adding an entry to the log failed, catastrophic failure")
			re.node.logger.Errorw("persist entry to raft log",
				append(re.logKV(), raftErrKeyword, err)...)
			re.node.signalFatalError(err)
		}
	}()

	var val []byte
	key := logEntryGetSerialisedKey(le)
	val, err = logEntryGetSerialised(le)
	if err != nil {
		return err
	}

	err = re.logDB.Update(func(tx *bolt.Tx) error {
		err := tx.Bucket([]byte(dbBucketLog)).Put(key, val)
		return err
	})

	return err
}

// logGetEntries returns up to a maximum of maxEntries entries from the log.
func (re *raftEngine) logGetEntries(startIndex int64, maxEntries int32) ([]*raft_pb.LogEntry, error) {

	var err error
	defer func() {
		if err != nil {
			err = raftErrorf(err, "fetch entries from log failed, catastrophic failure")
			re.node.logger.Errorw("fetch entries from raft log",
				append(re.logKV(), raftErrKeyword, err)...)
			re.node.signalFatalError(err)
		}
	}()

	results := make([]*raft_pb.LogEntry, 0, maxEntries)
	key := logEntryGetSerialisedKey(&raft_pb.LogEntry{Sequence: startIndex})

	err = re.logDB.View(func(tx *bolt.Tx) error {

		var err error

		iterator := tx.Bucket([]byte(dbBucketLog)).Cursor()
		for k, v := iterator.Seek(key); k != nil; k, v = iterator.Next() {

			var le *raft_pb.LogEntry
			le, err = logEntryFromSerialised(v)
			if err != nil {
				break
			}
			results = append(results, le)
			if int32(len(results)) == maxEntries {
				break
			}
		}

		return err
	})

	return results, err
}

func (re *raftEngine) logGetEntry(index int64) (*raft_pb.LogEntry, error) {

	key := logEntryGetSerialisedKey(&raft_pb.LogEntry{Sequence: index})

	var le *raft_pb.LogEntry

	err := re.logDB.View(func(tx *bolt.Tx) error {
		var err error
		data := tx.Bucket([]byte(dbBucketLog)).Get(key)
		if data != nil {
			le, err = logEntryFromSerialised(data)
		}
		return err
	})

	if err != nil {
		err = raftErrorf(err, "single entry from log failed to deserialise, corrupted data in bbolt db?")
		re.node.logger.Errorw("fetch single entry from raft log",
			append(re.logKV(), raftErrKeyword, err)...)
		re.node.signalFatalError(err)
	}

	return le, err
}

// logGetLastEntry returns last entry in the log if it exists, nil otherwise. Signal catastrophic failure if we
// fail to access persistence layer.
func (re *raftEngine) logGetLastEntry() (*raft_pb.LogEntry, error) {

	var le *raft_pb.LogEntry

	err := re.logDB.View(func(tx *bolt.Tx) error {

		var err error

		_, v := tx.Bucket([]byte(dbBucketLog)).Cursor().Last()
		if v != nil {
			le, err = logEntryFromSerialised(v)
		}

		return err
	})

	if err != nil {
		err = raftErrorf(err, "fetch last entry from log failed")
		re.node.logger.Errorw("fetch last entry from raft log",
			append(re.logKV(), raftErrKeyword, err)...)
		re.node.signalFatalError(err)
	}

	return le, err
}

// logGetLastTermAndIndex last term and index in log. On error, we signal failure back,
// and signal catastrophic failure.
func (re *raftEngine) logGetLastTermAndIndex() (term, index int64, err error) {

	lastLogIndex := indexNotSet
	lastLogTerm := termNotSet

	le, err := re.logGetLastEntry()
	if err != nil {
		return termNotSet, indexNotSet, err
	}

	if le != nil {
		lastLogTerm = le.Term
		lastLogIndex = le.Sequence
	}

	return lastLogTerm, lastLogIndex, nil
}

// logPurgeTailEntries deletes all entries from the startIndex and above, startIndex included.
func (re *raftEngine) logPurgeTailEntries(startIndex int64) error {

	key := logEntryGetSerialisedKey(&raft_pb.LogEntry{Sequence: startIndex})

	err := re.logDB.Update(func(tx *bolt.Tx) error {

		var err error

		bucket := tx.Bucket([]byte(dbBucketLog))
		iterator := bucket.Cursor()
		for k, _ := iterator.Seek(key); k != nil; k, _ = iterator.Next() {
			err = iterator.Delete()
			if err != nil {
				break
			}
		}

		return err
	})

	if err != nil {
		err = raftErrorf(err, "purge tail entries in log failed")
		re.node.logger.Errorw("purge tail entries from raft log",
			append(re.logKV(), raftErrKeyword, err)...)
		re.node.signalFatalError(err)
	}

	return err
}

// Bolt bucket names for logs and other persisted metadata.
const (
	dbBucketLog               = "Log"
	dbBucketNodePersistedData = "NodePersistedData"
)

func (re *raftEngine) nodePersistedDataGetSerialisedKey() []byte {
	return []byte(fmt.Sprintf("NodePersistedData[%d]", re.node.index))
}

// saveNodePersistedData saves node data which needs to be persisted into appropriate bucket in bbolt. This happens
// whenever we update any field in the persisted data. If this fails, we shut down asap... catastrophic failure.
func (re *raftEngine) saveNodePersistedData() error {

	key := re.nodePersistedDataGetSerialisedKey()

	ps := raft_pb.PersistedState{
		VotedFor:    re.votedFor.Load(),
		CurrentTerm: re.currentTerm.Load()}

	data, err := proto.Marshal(&ps)
	if err != nil {
		goto failed
	}

	err = re.logDB.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(dbBucketNodePersistedData)).Put(key, data)
	})

	if err != nil {
		goto failed
	}

	return err

failed:
	err = raftErrorf(err, "failed to save node persisted data")
	re.node.logger.Errorw("saving node persisted data",
		append(re.logKV(), raftErrKeyword, err)...)
	re.node.signalFatalError(err)

	return err
}

// loadNodePersistedData loads persisted data from BoltDB into the raftEngine structure.
// This happens at initialisation.
func (re *raftEngine) loadNodePersistedData() error {

	err := re.logDB.View(func(tx *bolt.Tx) error {

		bucket := tx.Bucket([]byte(dbBucketNodePersistedData))
		stream := bucket.Get(re.nodePersistedDataGetSerialisedKey())

		if stream == nil {
			return raftErrorf(RaftErrorNodePersistentData, "node persistent data missing for node %d",
				re.node.index)
		}

		ps := raft_pb.PersistedState{}
		err := proto.Unmarshal(stream, &ps)
		if err == nil {
			re.updateCurrentTerm(ps.CurrentTerm)
			re.votedFor.Store(ps.VotedFor)
		}

		return err
	})

	if err != nil {
		if errors.Cause(err) == RaftErrorNodePersistentData {
			// Let's initialise persisted state for the first time and save it.
			re.updateCurrentTerm(termNotSet)
			re.replaceTermIfNewer(0)
			return nil
		}
	}

	if err != nil {
		re.node.logger.Errorw("loading node persistent data, failed",
			append(re.logKV(), raftErrKeyword, err)...)
		re.node.signalFatalError(err)

	}

	return err
}

func (re *raftEngine) initLogDB(ctx context.Context, n *Node) error {

	f := n.config.LogDB

	opts := *bolt.DefaultOptions
	// Time to block trying to achieve flock on DB. We do not expect contention here, so we
	// provide and arbitrary small amount of time to avoid blocking indefinitely if a lock is
	// held on the DB (like when we try and run multiple instances of the same node).
	opts.Timeout = time.Second * 3

	n.logger.Debugw("opening bolt DB for persistence", n.logKV()...)
	var ldb *bolt.DB
	var err error

	ldb, err = bolt.Open(f, 0666, &opts)
	if err != nil {
		err = raftErrorf(err,
			"open bbolt DB for log entries failed (is another process using the DB?)")
		n.logger.Errorw("initialising DB for log entries", append(n.logKV(), raftErrKeyword, err)...)
		return err
	}

	err = ldb.Update(func(tx *bolt.Tx) error {
		_, err = tx.CreateBucketIfNotExists([]byte(dbBucketLog))
		return err
	})
	if err != nil {
		err = raftErrorf(err, "creating bbolt DB bucket for log entries failed")
		n.logger.Errorw("creating bucket DB for log entries", append(n.logKV(), raftErrKeyword, err)...)
		return err
	}

	err = ldb.Update(func(tx *bolt.Tx) error {
		_, err = tx.CreateBucketIfNotExists([]byte(dbBucketNodePersistedData))
		return err
	})
	if err != nil {
		err = raftErrorf(err, "creating bbolt DB bucket for persisted node data failed")
		n.logger.Errorw("creating bucket DB for persisted node data", append(n.logKV(), raftErrKeyword, err)...)
		return err
	}

	re.logDB = ldb

	err = re.loadNodePersistedData()
	if err != nil {
		return err
	}

	return nil
}

func (re *raftEngine) shutdownLogDB() {
	if re.logDB != nil {
		err := re.logDB.Close()
		if err != nil {
			err = raftErrorf(err, "logDB shutdown, bbotldb complained")
			re.node.logger.Errorw("", append(re.logKV(), raftErrKeyword, err)...)
		}
	}
}
