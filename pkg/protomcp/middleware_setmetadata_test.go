package protomcp

import (
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestGRPCRequest_SetMetadata_Sanitizes(t *testing.T) {
	g := &GRPCData{Metadata: metadata.MD{}}
	g.SetMetadata("x-user-id", "foo\r\nx-admin: 1")
	got := g.Metadata.Get("x-user-id")
	if len(got) != 1 {
		t.Fatalf("got %d values, want 1: %v", len(got), got)
	}
	if got[0] != "foox-admin: 1" {
		t.Errorf("value = %q, want CR/LF stripped", got[0])
	}
}

func TestGRPCRequest_SetMetadata_AllocatesMap(t *testing.T) {
	g := &GRPCData{} // nil Metadata
	g.SetMetadata("x", "clean")
	if got := g.Metadata.Get("x"); len(got) != 1 || got[0] != "clean" {
		t.Errorf("nil-Metadata path: got %v, want [\"clean\"]", got)
	}
}

func TestGRPCRequest_SetMetadata_NilReceiver(t *testing.T) {
	// No panic: the defensive nil-guard makes the helper safe to call
	// in middleware that may be invoked with a bogus request shape.
	var g *GRPCData
	g.SetMetadata("x", "v")
}
