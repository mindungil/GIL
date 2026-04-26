module github.com/jedutools/gil/cli

go 1.25.0

require (
	github.com/jedutools/gil/proto v0.0.0
	github.com/jedutools/gil/sdk v0.0.0-00010101000000-000000000000
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
	google.golang.org/grpc v1.65.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/net v0.25.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240528184218-531527333157 // indirect
	google.golang.org/protobuf v1.34.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/jedutools/gil/proto => ../proto
	github.com/jedutools/gil/sdk => ../sdk
)
