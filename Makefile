.PHONY: test vet build
test:
	go test -race -count=1 ./...
vet:
	go vet ./...
build:
	CGO_ENABLED=0 go build -o bin/turnrelay-udp ./cmd/turnrelay-udp
