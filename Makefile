BINARY := bitcoin-shard-proxy
SEND   := send-test-frames
RECV   := recv-test-frames

.PHONY: all test clean

all: $(BINARY) $(SEND) $(RECV)

$(BINARY):
	go build -o $(BINARY) .

$(SEND):
	go build -o $(SEND) ./cmd/send-test-frames/

$(RECV):
	go build -o $(RECV) ./cmd/recv-test-frames/

test:
	go test ./frame/... ./shard/...

clean:
	rm -f $(BINARY) $(SEND) $(RECV)
