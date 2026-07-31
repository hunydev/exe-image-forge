.PHONY: check test fmt dev e2e screenshots

check:
	cd vend && go test -race ./...
	cd vend && go vet ./...
	test -z "$$(gofmt -l vend/*.go)"
	bash -n exe-image-forge install.sh image/files/update-ai-clis \
		image/files/write-agent-context image/files/write-versions \
		image/files/init-wrapper.sh

test:
	cd vend && go test ./...

fmt:
	gofmt -w vend/*.go

dev:
	cd vend && go run . -demo -addr 127.0.0.1:18080

e2e:
	./scripts/run-e2e.sh

screenshots:
	./scripts/run-e2e.sh --project=chromium --grep @screenshots
