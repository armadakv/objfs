MODULES := . s3 gcs azblob

.PHONY: all
all: check test

.PHONY: test
test:
	@for mod in $(MODULES); do \
		echo "Testing $$mod"; \
		(cd $$mod && go test ./... -cover -race -v) || exit 1; \
	done

.PHONY: test-integration
test-integration:
	@for mod in $(MODULES); do \
		echo "Testing integration $$mod"; \
		(cd $$mod && go test -tags=integration ./... -cover -race -v) || exit 1; \
	done

.PHONY: check
check:
	@echo "Running check"
ifeq (, $(shell which golangci-lint))
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin v1.57.2
endif
	@for mod in $(MODULES); do \
		echo "Checking $$mod"; \
		(cd $$mod && golangci-lint run) || exit 1; \
	done

.PHONY: fmt
fmt:
	@for mod in $(MODULES); do \
		echo "Formatting $$mod"; \
		(cd $$mod && gofumpt -w . && goimports -w -local github.com/armadakv/objfs .) || exit 1; \
	done
