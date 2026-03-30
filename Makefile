BINARY := bitcoin-shard-proxy
SEND   := send-test-frames
RECV   := recv-test-frames

.PHONY: all test test-e2e clean

all: $(BINARY) $(SEND) $(RECV)

$(BINARY):
	go build -o $(BINARY) .

$(SEND):
	go build -o $(SEND) ./cmd/send-test-frames/

$(RECV):
	go build -o $(RECV) ./cmd/recv-test-frames/

test:
	go test ./frame/... ./shard/...

test-e2e: $(BINARY) $(SEND) $(RECV)
	PATH="$(CURDIR):$$PATH" sh test/run-e2e.sh

clean:
	rm -f $(BINARY) $(SEND) $(RECV)
