.PHONY: default fmt test test-spike test-e2e build lint

define run
	@fail=0; \
		printf '➡️  make %-12s ' "$(1) ..."; \
		if out=$$($(MAKE) $(1) 2>&1); then \
			echo "✅"; \
		else \
			echo "❌"; \
			printf '%s\n' "$$out" | sed 's/^/    /'; \
			fail=1; \
		fi; \
	exit $$fail
endef

default:
	$(call run,lint)
	$(call run,test)
	$(call run,build)

fmt:
	golangci-lint fmt

test:
	go test ./...

test-spike:
	go test -tags spike -count=1 -v -timeout 15m ./test/spike/...

# Runs stacks on the local Docker daemon from the released images.
test-e2e:
	go test -tags e2e -count=1 -v -timeout 30m ./test/e2e/...

build:
	go build -o build/tule ./cmd/tule

lint:
	golangci-lint run
