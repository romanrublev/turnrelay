.PHONY: test vet build integration singbox
test:
	go test -race -count=1 ./...
vet:
	go vet ./...
build:
	CGO_ENABLED=0 go build -o bin/turnrelay-udp ./cmd/turnrelay-udp
# Docker interop test against coturn, the unmodified anton48 SRTP server and
# a real WireGuard. Manual, not part of go test.
integration:
	bash test/integration/run.sh
singbox:
	cd singbox && go vet ./... && go test -race -count=1 ./...
