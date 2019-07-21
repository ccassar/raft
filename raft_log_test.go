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

func TestLogReplication(t *testing.T) {

	electionPeriod := time.Millisecond * 500
	wait := electionPeriod * 30
	nodeCount := 3
	for i := 0; i < nodeCount; i++ {
		os.Remove(fmt.Sprintf("test/boltdb.%d", i))
	}

	n := make([]*testNode, nodeCount)
	nodes := constNodes
	var err error

	for i := 0; i < len(nodes); i++ {
		fmt.Println(" ***************  Bring up ", i)
		n[i], err = testAddNode(nodes, i, electionPeriod)
		if err != nil {
			t.Fatal("failed to start node")
		}
	}

	// Produce an entry at the leader first and receive it, elsewhere.
	leaderIndex := testFindNewLeader(n, wait, false)
	if leaderIndex == noLeader {
		t.Fatal("failed to elect a leader")
	}

	producerIndex := leaderIndex + 1
	if producerIndex >= len(n) {
		producerIndex = 0
	}

	messages := 100
	msgContent := "Daisy, daisy..."

	// Test both producing from follower and producing from leader...
	producers := []*Node{n[producerIndex].node, n[leaderIndex].node}
	for _, producer := range producers {
		fmt.Printf(" ***************  Producing msg from %d to %d: %s\n", producerIndex, leaderIndex, string(msgContent))

		produced := 0
		for msgCount := 0; msgCount < messages; msgCount++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = producer.LogProduce(ctx, []byte(msgContent))
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			produced++
		}
		fmt.Printf(" ***************  Produced %d msgs from %d to %d: %s\n",
			produced, producerIndex, leaderIndex, string(msgContent))

		//
		// Next... let's collect the result from all...
		for msgCount := 0; msgCount < messages; msgCount++ {
			for i := 0; i < nodeCount; i++ {
				select {
				case msg := <-n[i].node.config.LogCmds:
					if string(msg) != msgContent {
						t.Fatal("expected v rxed: ", string(msgContent), string(msg))
					}
				}
			}
		}
	}

	// Destroy leader node and exercise resync...
	fmt.Println(" ***************  Destroying leader:", leaderIndex)
	n[leaderIndex].cancel()
	n[leaderIndex].wg.Wait()

	// Produce an entry at the leader first and receive it, elsewhere.
	newLeaderIndex := testFindNewLeader(n, wait, true)
	if leaderIndex == noLeader {
		t.Fatal("failed to elect a new leader")
	}
	fmt.Println(" ***************  New leader:", newLeaderIndex)

	fmt.Println("Resuscitating old leader node:", leaderIndex)
	n[leaderIndex], err = testAddNode(nodes, leaderIndex, electionPeriod)
	for msgCount := 0; msgCount < messages*len(producers); msgCount++ {
		select {
		case msg := <-n[leaderIndex].node.config.LogCmds:
			if string(msg) != msgContent {
				t.Fatal("expected v rxed: ", string(msgContent), string(msg))
			}
		}
	}

	fmt.Println(" ***************  Test recovery of follower with missed updates")
	fmt.Println(" ***************  Destroying follower (used to be leader):", leaderIndex)
	n[leaderIndex].cancel()
	n[leaderIndex].wg.Wait()

	fmt.Printf(" ***************  Producing msg from %d: %s\n", producerIndex, string(msgContent))
	producer := n[producerIndex]
	produced := 0
	for msgCount := 0; msgCount < messages; msgCount++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = producer.node.LogProduce(ctx, []byte(msgContent))
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		produced++
	}
	fmt.Printf(" ***************  Produced %d msgs from %d: %s\n",
		produced, producerIndex, string(msgContent))

	fmt.Println("Resuscitating old follower (used to be leader) node:", leaderIndex)
	n[leaderIndex], err = testAddNode(nodes, leaderIndex, electionPeriod)

	for msgCount := 0; msgCount < messages*(len(producers)+1); msgCount++ {
		select {
		case msg := <-n[leaderIndex].node.config.LogCmds:
			if string(msg) != msgContent {
				t.Fatal("expected v rxed: ", string(msgContent), string(msg))
			}
		}
	}

	fmt.Println(" ***************  Shutting down")
	for i := 0; i < nodeCount; i++ {
		if n[i] == nil {
			continue
		}
		if n[i].cancel != nil {
			fmt.Println("Cancelling: ", i)
			n[i].cancel()
		}
		fmt.Println("Waiting for: ", i)
		n[i].wg.Wait()
	}

}

func ExampleNode_LogProduce() {

	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())

	cfg := NodeConfig{
		Nodes:   []string{"node1.example.com:443", "node2.example.com:443", "node3.example.com:443"},
		LogCmds: make(chan []byte, 32),
		LogDB:   "mydb.bbolt",
	}
	wg.Add(1)
	localIndex := int32(2) // say, if we are node3.example.com
	n, err := MakeNode(ctx, &wg, cfg, localIndex)
	if err != nil {
	}

	// Kick off handling of LogCmds and FatalErrorChannel().
	// ...
	//
	// Produce log command to distributed log.
	ctxLogProduce, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = n.LogProduce(ctxLogProduce, []byte("I'm sorry Dave..."))
	cancel()
	if err != nil {
		// Do retry here, preferably using exponentially backoff.
	}

	// When we're done...
	cancel()
	wg.Wait()
}
