GOLANGCI_LINT=1.51.2
lint:
	docker run --rm -v $$(pwd):/app -v ~/.cache/golangci-lint/v$(GOLANGCI_LINT):/root/.cache -w /app golangci/golangci-lint:v$(GOLANGCI_LINT) golangci-lint run
