.PHONY: build test install mac

build:
	go build -o focolock ./cmd/focolock

test:
	go test -race ./...

# Binário universal (Intel + Apple Silicon). Rodar num Mac.
mac:
	GOOS=darwin GOARCH=arm64 go build -o dist/focolock-arm64 ./cmd/focolock
	GOOS=darwin GOARCH=amd64 go build -o dist/focolock-amd64 ./cmd/focolock
	lipo -create -output dist/focolock dist/focolock-arm64 dist/focolock-amd64

install: build
	sudo ./focolock instalar
