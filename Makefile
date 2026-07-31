.PHONY: check test fmt

check:
	cd vend && go test ./...
	cd vend && go vet ./...
	test -z "$$(gofmt -l vend/*.go)"
	bash -n exe-image-forge install.sh image/files/update-ai-clis \
		image/files/write-agent-context image/files/write-versions \
		image/files/init-wrapper.sh

test:
	cd vend && go test ./...

fmt:
	gofmt -w vend/*.go
