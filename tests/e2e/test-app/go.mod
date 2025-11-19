module test-app

go 1.25.3

require (
	github.com/gorilla/websocket v1.5.3
	github.com/rs/zerolog v1.34.0
	gosuda.org/portal v1.4.4
)

require (
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/planetscale/vtprotobuf v0.6.0 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	golang.org/x/crypto v0.43.0 // indirect
	golang.org/x/sys v0.37.0 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)

replace gosuda.org/portal => ../../../
