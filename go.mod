module github.com/gdsoumya/protomcp

go 1.26.2

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.11-20260415201107-50325440f8f2.1
	github.com/google/jsonschema-go v0.4.2
	// TODO: bump to the first tagged release of modelcontextprotocol/go-sdk
	// that contains the ServerSession.startKeepalive race fix (PR #856, commit
	// 862d78a on main). Until then we ride a main-branch pseudo-version, which
	// means `go get -u` can pull in arbitrary upstream changes.
	github.com/modelcontextprotocol/go-sdk v1.5.1-0.20260414145311-4c00594be629
	google.golang.org/genproto/googleapis/api v0.0.0-20260414002931-afd174a4e478
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.33.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260406210006-6f92a3bedf2d // indirect
)
