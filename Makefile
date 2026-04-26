.PHONY: tidy test gen build clean

tidy:
	@for m in core runtime proto server cli tui sdk mcp; do \
		(cd $$m && go mod tidy) || exit 1; \
	done

test:
	@for m in core runtime proto server cli tui sdk mcp; do \
		(cd $$m && go test ./...) || exit 1; \
	done

gen:
	@cd proto && buf generate

build:
	@mkdir -p bin
	@cd cli && go build -o ../bin/gil ./cmd/gil
	@cd server && go build -o ../bin/gild ./cmd/gild

clean:
	@rm -rf bin
