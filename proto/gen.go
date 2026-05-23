//go:generate protoc --proto_path=. --go_out=../internal/grpc/gen --go_opt=paths=source_relative --go-grpc_out=../internal/grpc/gen --go-grpc_opt=paths=source_relative types.proto node.proto control.proto

// Package proto holds .proto source definitions.
// Run `go generate ./proto/...` from the module root to regenerate Go stubs.
package proto
