module github.com/mindungil/gil/cli

go 1.25.0

require (
	github.com/mindungil/gil/core v0.0.0-00010101000000-000000000000
	github.com/mindungil/gil/proto v0.0.0
	github.com/mindungil/gil/sdk v0.0.0-00010101000000-000000000000
	github.com/mindungil/gil/tui v0.0.0-00010101000000-000000000000
	github.com/oklog/ulid/v2 v2.1.1
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
	golang.org/x/term v0.42.0
	google.golang.org/grpc v1.65.0
	google.golang.org/protobuf v1.34.2
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/anthropics/anthropic-sdk-go v1.38.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.22.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/invopop/jsonschema v0.13.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
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
	github.com/mindungil/gil/tui => ../tui
)
