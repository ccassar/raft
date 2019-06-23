package raft

import (
	"context"
	"fmt"
	"github.com/ccassar/raft/internal/raft_pb"
	"go.etcd.io/bbolt"
	"go.uber.org/atomic"
	"testing"
)

func TestLogDBBasicOperations(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	n := &Node{
		logger:          testLoggerGet().Sugar(),
		messaging:       &raftMessaging{},
		fatalErrorCount: atomic.NewInt32(0),
		config: &NodeConfig{
			LogDB:   "test/boltdb.mydb",
			LogCmds: make(chan []byte)}}
	re := raftEngine{node: n}

	t.Log("Initialise persistent raft log")
	err := re.initLogDB(ctx, n)
	if err != nil {
		t.Fatal(err)
	}

	if re.logDB == nil {
		t.Fatal("Test failed to create logDB")
	}

	t.Log("Test adding log entries")
	var i int64
	addCount := 1001
	for i = 1; i < int64(addCount+1); i++ {
		le := raft_pb.LogEntry{
			Sequence: i,
		}
		err = re.logEntryAdd(&le)
		if err != nil {
			t.Error(err)
		}
	}

	t.Log("BoltDB Stats:", fmt.Sprintf("%+v", re.logDB.Stats()))

	countEntries := func() int {
		start := int64(1)
		count := 0
		batchSize := 17
		for {
			res, err := re.logEntriesGet(start, batchSize)
			if err != nil {
				t.Fatal(err)
			}
			if len(res) == 0 {
				break
			}
			count = count + len(res)
			start = start + int64(len(res))
		}
		return count
	}

	count := countEntries()
	if count != addCount {
		t.Errorf("Test added %v entries, and got back %v", addCount, count)
	}

	t.Log("Test order of log entries, low level")
	var last int64
	err = re.logDB.View(func(tx *bbolt.Tx) error {
		iterator := tx.Bucket([]byte(dbBucketLog)).Cursor()
		for k, _ := iterator.First(); k != nil; k, _ = iterator.Next() {
			current, _ := sequenceFromSerialisedKey(k)
			if current < last {
				t.Errorf("Test found unordered entries produced by serialisation: %v before %v",
					last, current)
			}
			last = current
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}

	t.Log("Test purging just the last entry")
	err = re.logEntriesPurgeTail(1001)
	if err != nil {
		t.Error(err)
	}

	count = countEntries()
	if count != addCount-1 {
		t.Errorf("Test added %v entries, removed 1, and got back %v", addCount, count)
	}

	t.Log("Test purging all entries")
	err = re.logEntriesPurgeTail(1)
	if err != nil {
		t.Error(err)
	}

	count = countEntries()
	if count != 0 {
		t.Errorf("Test removed all and got back %v", count)
	}

	re.shutdownLogDB()
}
