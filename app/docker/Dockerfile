
FROM golang:1.12 AS builder
RUN go get -d github.com/ccassar/raft/...
WORKDIR /go/src/github.com/ccassar/raft/app
RUN go install -v ./...

# Minimal ubuntu distribution. Go Alpine for really minimal...
FROM ubuntu:19.10
WORKDIR /root/
COPY --from=builder /go/bin/app /root/
CMD ["./app"]