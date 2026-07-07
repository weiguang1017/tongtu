.PHONY: build linux clean

build:
	go build -o bin/tongtu ./cmd/tongtu
	go build -o bin/tongtud ./cmd/tongtud

linux:
	GOOS=linux GOARCH=amd64 go build -o bin/tongtud-linux-amd64 ./cmd/tongtud
	GOOS=linux GOARCH=arm64 go build -o bin/tongtud-linux-arm64 ./cmd/tongtud

clean:
	rm -rf bin
