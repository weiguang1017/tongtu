.PHONY: build linux clean vet

build:
	go build -o bin/tongtu ./cmd/tongtu

linux:
	GOOS=linux GOARCH=amd64 go build -o bin/tongtu-linux-amd64 ./cmd/tongtu
	GOOS=linux GOARCH=arm64 go build -o bin/tongtu-linux-arm64 ./cmd/tongtu

vet:
	go vet ./...

clean:
	rm -rf bin
