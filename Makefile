.PHONY: default fmt test test-spike build lint

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

build:
	go build -o build/tule ./cmd/tule

lint:
	golangci-lint run
