.PHONY: tidy test gen build clean e2e

tidy:
	@for m in core runtime proto server cli tui sdk mcp; do \
		(cd $$m && go mod tidy) || exit 1; \
	done

test:
	@for m in core runtime proto server cli tui sdk mcp; do \
		if [ -f "$$m/go.mod" ] && find "$$m" -maxdepth 2 -name "*.go" | grep -q .; then \
			(cd $$m && go test ./...) || exit 1; \
		fi; \
	done

gen:
	@cd proto && buf generate

build:
	@mkdir -p bin
	@cd cli && go build -o ../bin/gil ./cmd/gil
	@cd server && go build -o ../bin/gild ./cmd/gild

e2e: build
	@bash tests/e2e/phase01_test.sh

clean:
	@rm -rf bin
