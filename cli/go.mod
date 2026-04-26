module github.com/jedutools/gil/cli

go 1.22.2

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jedutools/gil/sdk v0.0.0
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
)

replace github.com/jedutools/gil/sdk => ../sdk
