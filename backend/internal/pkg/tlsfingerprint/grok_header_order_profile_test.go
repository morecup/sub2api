package tlsfingerprint

import (
	"reflect"
	"testing"
)

func TestGrokCLIHTTP2ProfileExposesCapturedHeaderOrderExpectations(t *testing.T) {
	profile := GrokCLIHTTP2Profile()
	if profile == nil {
		t.Fatal("GrokCLIHTTP2Profile returned nil")
	}

	wantPseudo := []string{":method", ":scheme", ":authority", ":path"}
	wantRegular := []string{
		"content-type",
		"user-agent",
		"x-compactions-remaining",
		"x-compaction-at",
		"x-grok-client-version",
		"x-grok-user-id",
		"x-grok-client-identifier",
		"authorization",
		"traceparent",
		"x-grok-conv-id",
		"x-grok-req-id",
		"x-grok-model-override",
		"x-grok-session-id",
		"x-grok-agent-id",
		"x-grok-turn-idx",
		"accept",
		"accept-encoding",
		"content-length",
	}

	val := reflect.ValueOf(profile).Elem()
	assertReflectedStringSliceField(t, val, "PseudoHeaderOrder", wantPseudo)
	assertReflectedStringSliceField(t, val, "RegularHeaderOrder", wantRegular)
}

func assertReflectedStringSliceField(t *testing.T, val reflect.Value, fieldName string, want []string) {
	t.Helper()

	field := val.FieldByName(fieldName)
	if !field.IsValid() {
		t.Fatalf("HTTP2Profile missing %s; Phase 4 needs explicit profile-level header ordering expectations", fieldName)
	}
	if field.Kind() != reflect.Slice {
		t.Fatalf("HTTP2Profile.%s kind = %s, want slice", fieldName, field.Kind())
	}
	got := make([]string, field.Len())
	for i := 0; i < field.Len(); i++ {
		if field.Index(i).Kind() != reflect.String {
			t.Fatalf("HTTP2Profile.%s[%d] kind = %s, want string", fieldName, i, field.Index(i).Kind())
		}
		got[i] = field.Index(i).String()
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP2Profile.%s = %v, want %v", fieldName, got, want)
	}
}
