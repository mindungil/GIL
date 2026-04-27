module github.com/mindungil/gil/cli

go 1.25.0

require (
	github.com/mindungil/gil/core v0.0.0-00010101000000-000000000000
	github.com/mindungil/gil/proto v0.0.0
	github.com/mindungil/gil/sdk v0.0.0-00010101000000-000000000000
	github.com/oklog/ulid/v2 v2.1.1
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
	golang.org/x/term v0.42.0
	google.golang.org/grpc v1.65.0
	google.golang.org/protobuf v1.34.2
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.22.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240814211410-ddb44dafa142 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240814211410-ddb44dafa142 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/mindungil/gil/core => ../core
	github.com/mindungil/gil/proto => ../proto
	github.com/mindungil/gil/sdk => ../sdk
)
