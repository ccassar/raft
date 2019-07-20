### Test Application

The source in this application is a simple skeleton showing basic features of the raft package in action.
Applications running in a cluster will all see a single version of a distributed log and can each contribute
to that log. The distributed log is dumped to stdout. The dummy messages include the originating node and a
UUID.

In order to run one instance in the application cluster, with debug/logging written to file, you might invoke as
follows:

```
./app -config=app.json  -localNode=2 -zapFile=/tmp/log -debug
```

The output generated to stdout looks as follows:

```

./app -config=test/app.json -localNode=0 -debug 2>/tmp/log0.redir
Node0:29463c76-159a-43bd-8aa0-c0c654e67f69
Node0:832a806b-2cab-47d5-9a79-2a075f56324e
Node1:54caaacb-b585-4b01-8db4-9c3739d1c4ba
Node2:2740efcb-df3f-4c04-b3d0-b1c7ed163bc3
Node0:d6adc157-27c3-45de-9c91-f4d83ea2d19f
Node1:00bdafa3-30d4-4be8-bb06-3472927ad00a
Node0:3c732fc7-a31e-4a8d-8a01-c8199df058fd
...
```

By default, application logs to stderr, so redirecting stderr to file achieves the same end as `-zapFile` command line
option.


