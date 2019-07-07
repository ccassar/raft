package raft

import (
	"context"
	"fmt"
	"github.com/ccassar/raft/internal/raft_pb"
	"go.etcd.io/bbolt"
	"go.uber.org/atomic"
	"os"
	"sync"
	"testing"
	"time"
)

func TestLogDBBasicOperations(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const testDB = "test/boltdb.mydb"

	os.Remove(testDB)
	n := &Node{
		logger:          testLoggerGet().Sugar(),
		messaging:       &raftMessaging{},
		fatalErrorCount: atomic.NewInt32(0),
		config: &NodeConfig{
			LogDB:   testDB,
			LogCmds: make(chan []byte)}}

	t.Log("Initialise persistent raft engine")
	err := initRaftEngine(ctx, n)
	if err != nil {
		t.Fatal(err)
	}

	re := n.engine
	if re.logDB == nil {
		t.Fatal("Test failed to create logDB")
	}

	term, index, err := re.logGetLastTermAndIndex()
	if err != nil {
		t.Errorf("expect to be able to get last term and index even before we are set [%v]", err)
	}
	if term != termNotSet {
		t.Errorf("expect unset term [%v]", term)
	}
	if index != indexNotSet {
		t.Errorf("expect unset index [%v]", index)
	}

	t.Log("Test adding log entries")
	var i int64
	addCount := int64(1001)
	for i = 1; i < addCount+1; i++ {
		le := raft_pb.LogEntry{
			Sequence: i,
		}
		err = re.logAddEntry(&le)
		if err != nil {
			t.Error(err)
		}
	}

	term, index, err = re.logGetLastTermAndIndex()
	if err != nil {
		t.Errorf("expect to be able to get last term and index [%v]", err)
	}
	fmt.Println(term, index)

	t.Log("BoltDB Stats:", fmt.Sprintf("%+v", re.logDB.Stats()))
	if index != addCount {
		t.Errorf("expect index [%v] got [%v]", addCount, index)
	}

	countEntries := func() int {
		start := int64(1)
		count := 0
		batchSize := int32(17)
		for {
			res, err := re.logGetEntries(start, batchSize)
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
	if int64(count) != addCount {
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
	err = re.logPurgeTailEntries(1001)
	if err != nil {
		t.Error(err)
	}

	count = countEntries()
	if int64(count) != addCount-1 {
		t.Errorf("Test added %v entries, removed 1, and got back %v", addCount, count)
	}

	t.Log("Test purging all entries")
	err = re.logPurgeTailEntries(1)
	if err != nil {
		t.Error(err)
	}

	count = countEntries()
	if count != 0 {
		t.Errorf("Test removed all and got back %v", count)
	}

	re.shutdownLogDB()
}

func TestAcknowledgements(t *testing.T) {
	const BASE = 10
	const ACKS = 100
	target := atomic.NewInt64(0)
	l := testLoggerGet()
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	wg.Add(1)

	acker := createLogAcknowledgerAndRun(ctx, &wg, target, l.Sugar())

	var cmds []*logCommandContainer
	for i := 0; i < ACKS; i++ {
		cmds = append(cmds, &logCommandContainer{returnChan: make(chan *logCommandContainer, 1)})
	}

	for i, cmd := range cmds {
		acker.trackPendingAck(cmd, int64(i+BASE))
	}

	// Notify...
	acker.notify()

	// We wait enough to know that the notification itself was handled. We know the notification is being
	// handled when we find the space to issue another notification (strictly not a guarantee that the
	// next test is water tight but good enough).
	acker.updatesAvailable <- struct{}{}

	// At this point we should not get anything on the channels.
	for i := 0; i < ACKS; i++ {
		select {
		case <-cmds[i].returnChan:
			t.Fatal("not expecting to receive and acknowledgements while target is set to 0")
		default:
		}
	}

	// At this point we should only get update from the first...
	target.Store(BASE)
	acker.notify()
	select {
	case cmd := <-cmds[0].returnChan:
		if !cmd.reply.Ack {
			t.Fatal("expected to get ack set")
		}
	case <-time.After(time.Second):
		t.Fatal("expected to get ack within at least the timeout")
	}

	cancel()
	wg.Wait()

	// we should get error on all the rest (i.e. bar the first which would have been discarded by now.
	for i := 1; i < ACKS; i++ {
		select {
		case <-cmds[i].returnChan:
			if cmds[i].err == nil {
				t.Fatal("not expecting to receive an acknowledgement but an error on cancellation")
			}
		case <-time.After(time.Second):
			t.Fatal("expected to get nack within at least the timeout")
		}
	}

}
