// External test package so we can import the example proto fixture
// (which itself depends on protomcp) without an import cycle.
package protomcp_test

import (
	"testing"

	authv1 "github.com/gdsoumya/protomcp/pkg/api/gen/examples/auth/v1"
	"github.com/gdsoumya/protomcp/pkg/protomcp"
)

// TestClearOutputOnly_TopLevelScalar — the canonical case: a message
// whose only fields are OUTPUT_ONLY scalars. Every field must be zeroed.
func TestClearOutputOnly_TopLevelScalar(t *testing.T) {
	m := &authv1.WhoAmIResponse{UserId: "alice", Tenant: "acme"}
	protomcp.ClearOutputOnly(m)
	if m.UserId != "" {
		t.Errorf("UserId = %q, want empty", m.UserId)
	}
	if m.Tenant != "" {
		t.Errorf("Tenant = %q, want empty", m.Tenant)
	}
}

// TestClearOutputOnly_NestedMessage — OUTPUT_ONLY inside a nested
// message field. This is the bug the original review was worried
// about: without recursion, the inner field leaks through.
func TestClearOutputOnly_NestedMessage(t *testing.T) {
	m := &authv1.TestNested{
		Inner: &authv1.TestInner{
			ServerId: "admin", // OUTPUT_ONLY — must be cleared
			UserName: "alice", // regular — must survive
		},
	}
	protomcp.ClearOutputOnly(m)
	if m.Inner == nil {
		t.Fatalf("Inner was cleared entirely; only ServerId should be")
	}
	if m.Inner.ServerId != "" {
		t.Errorf("Inner.ServerId = %q, want empty (nested OUTPUT_ONLY leaked)", m.Inner.ServerId)
	}
	if m.Inner.UserName != "alice" {
		t.Errorf("Inner.UserName = %q, want %q (regular field wiped)", m.Inner.UserName, "alice")
	}
}

// TestClearOutputOnly_OutputOnlyMessageField — when the MESSAGE field
// itself is OUTPUT_ONLY, the whole nested value is cleared. No recursion
// needed (or performed).
func TestClearOutputOnly_OutputOnlyMessageField(t *testing.T) {
	m := &authv1.TestOutputOnlyMessage{
		Stripped: &authv1.TestInner{ServerId: "x", UserName: "y"},
		Kept:     "surface",
	}
	protomcp.ClearOutputOnly(m)
	if m.Stripped != nil {
		t.Errorf("Stripped = %+v, want nil", m.Stripped)
	}
	if m.Kept != "surface" {
		t.Errorf("Kept = %q, want %q", m.Kept, "surface")
	}
}

// TestClearOutputOnly_RepeatedMessages — recursive clearing must reach
// every element of a repeated<Message> field.
func TestClearOutputOnly_RepeatedMessages(t *testing.T) {
	m := &authv1.TestRepeatedMessages{
		Items: []*authv1.TestInner{
			{ServerId: "s1", UserName: "u1"},
			{ServerId: "s2", UserName: "u2"},
			{ServerId: "s3", UserName: "u3"},
		},
	}
	protomcp.ClearOutputOnly(m)
	if len(m.Items) != 3 {
		t.Fatalf("Items length = %d, want 3 (list itself should not be cleared)", len(m.Items))
	}
	for i, it := range m.Items {
		if it.ServerId != "" {
			t.Errorf("Items[%d].ServerId = %q, want empty", i, it.ServerId)
		}
		if it.UserName == "" {
			t.Errorf("Items[%d].UserName was wiped", i)
		}
	}
}

// TestClearOutputOnly_MapMessages — each message-valued entry in a
// map must be recursively cleared; the map keys and structure survive.
func TestClearOutputOnly_MapMessages(t *testing.T) {
	m := &authv1.TestMapMessages{
		Items: map[string]*authv1.TestInner{
			"alpha": {ServerId: "sa", UserName: "a"},
			"beta":  {ServerId: "sb", UserName: "b"},
		},
	}
	protomcp.ClearOutputOnly(m)
	if len(m.Items) != 2 {
		t.Fatalf("Items length = %d, want 2", len(m.Items))
	}
	for k, v := range m.Items {
		if v.ServerId != "" {
			t.Errorf("Items[%q].ServerId = %q, want empty", k, v.ServerId)
		}
		if v.UserName == "" {
			t.Errorf("Items[%q].UserName was wiped", k)
		}
	}
}

// TestClearOutputOnly_OneofOutputOnlySelected — if the OUTPUT_ONLY
// member of a oneof is the currently-selected one, the oneof must be
// cleared. Has() on the oneof reports false afterwards.
func TestClearOutputOnly_OneofOutputOnlySelected(t *testing.T) {
	m := &authv1.TestOneofOutputOnly{
		Choice: &authv1.TestOneofOutputOnly_Computed{Computed: "server-picked"},
	}
	protomcp.ClearOutputOnly(m)
	if m.Choice != nil {
		t.Errorf("Choice = %+v, want nil after OUTPUT_ONLY oneof member cleared", m.Choice)
	}
}

// TestClearOutputOnly_OneofRegularSelected — when a non-OUTPUT_ONLY
// oneof member is selected, Clear on the OUTPUT_ONLY sibling is a
// no-op. The selected member must survive.
func TestClearOutputOnly_OneofRegularSelected(t *testing.T) {
	m := &authv1.TestOneofOutputOnly{
		Choice: &authv1.TestOneofOutputOnly_Manual{Manual: "user-picked"},
	}
	protomcp.ClearOutputOnly(m)
	manual, ok := m.Choice.(*authv1.TestOneofOutputOnly_Manual)
	if !ok {
		t.Fatalf("Choice = %T, want *TestOneofOutputOnly_Manual", m.Choice)
	}
	if manual.Manual != "user-picked" {
		t.Errorf("Manual = %q, want %q", manual.Manual, "user-picked")
	}
}

// TestClearOutputOnly_RepeatedScalarOutputOnly — OUTPUT_ONLY on a
// repeated scalar field clears the entire list.
func TestClearOutputOnly_RepeatedScalarOutputOnly(t *testing.T) {
	m := &authv1.TestRepeatedOutputOnlyScalar{
		ServerIds: []string{"sid-1", "sid-2"},
		Names:     []string{"alice", "bob"},
	}
	protomcp.ClearOutputOnly(m)
	if len(m.ServerIds) != 0 {
		t.Errorf("ServerIds = %v, want empty", m.ServerIds)
	}
	if len(m.Names) != 2 {
		t.Errorf("Names = %v, want length 2 (non-OUTPUT_ONLY list wiped)", m.Names)
	}
}

// TestClearOutputOnly_NoLeafAnnotation — recursing into a message
// type with no OUTPUT_ONLY fields anywhere must be a no-op but not
// panic. WhoAmIRequest is empty; TestInner has a user field that
// must survive when called directly.
func TestClearOutputOnly_NoChangeForRegularFields(t *testing.T) {
	m := &authv1.TestInner{UserName: "alice"}
	protomcp.ClearOutputOnly(m)
	if m.UserName != "alice" {
		t.Errorf("UserName = %q, want alice (nothing should have changed)", m.UserName)
	}
}

// TestClearOutputOnly_NilSafe — generated code should never pass nil,
// but the helper is defensive.
func TestClearOutputOnly_NilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ClearOutputOnly(nil) panicked: %v", r)
		}
	}()
	protomcp.ClearOutputOnly(nil)
}

// TestClearOutputOnly_UnsetNestedNotRecursed — if a nested message
// field is unset (nil), we must NOT try to recurse into it (would
// panic on a nil reflect.Message). Has() gate protects us.
func TestClearOutputOnly_UnsetNestedNotRecursed(t *testing.T) {
	m := &authv1.TestNested{} // Inner is nil
	protomcp.ClearOutputOnly(m)
	if m.Inner != nil {
		t.Errorf("Inner = %+v, want nil (we shouldn't materialize it)", m.Inner)
	}
}

// TestClearOutputOnly_EmptyRepeatedNotRecursed — empty repeated/map
// must be skipped cleanly without iterating.
func TestClearOutputOnly_EmptyCollectionsSkipped(t *testing.T) {
	rm := &authv1.TestRepeatedMessages{}
	mm := &authv1.TestMapMessages{}
	protomcp.ClearOutputOnly(rm)
	protomcp.ClearOutputOnly(mm)
	if len(rm.Items) != 0 {
		t.Errorf("Repeated items mutated: %v", rm.Items)
	}
	if len(mm.Items) != 0 {
		t.Errorf("Map items mutated: %v", mm.Items)
	}
}

// TestBoolPtr — basic coverage of the helper used by the generator.
func TestBoolPtr(t *testing.T) {
	if p := protomcp.BoolPtr(true); p == nil || *p != true {
		t.Errorf("BoolPtr(true) = %v", p)
	}
	if p := protomcp.BoolPtr(false); p == nil || *p != false {
		t.Errorf("BoolPtr(false) = %v", p)
	}
}
