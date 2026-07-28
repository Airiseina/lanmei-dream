package plugin

import (
	"strings"
	"testing"
	"time"
)

// ── PluginInfoResponse.Validate ──

func TestPluginInfoResponseValidate_OK(t *testing.T) {
	r := &PluginInfoResponse{
		ABIVersion:  ABIVersion,
		ID:          "signin",
		Name:        "签到",
		Description: "每日签到",
		Version:     "1.0.0",
		Commands:    []CommandDecl{{Name: "签到", Description: "每日试试手气"}},
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPluginInfoResponseValidate_ABIError(t *testing.T) {
	r := &PluginInfoResponse{
		ABIVersion: "lanmei.plugin/v2",
		ID:         " signin ",
		Name:       "x",
		Version:    "1.0.0",
		Commands:   []CommandDecl{{Name: "c"}},
	}
	if err := r.Validate(); err == nil {
		t.Fatal("expected ABI error")
	}
}

func TestPluginInfoResponseValidate_IDPattern(t *testing.T) {
	tests := []string{"", "SIGNIN", "123", "a-b", "a b", "a!b"}
	for _, id := range tests {
		r := &PluginInfoResponse{
			ABIVersion: ABIVersion, ID: id, Name: "x", Version: "1.0.0",
			Commands: []CommandDecl{{Name: "c"}},
		}
		if err := r.Validate(); err == nil {
			t.Errorf("id=%q should be rejected", id)
		}
	}
}

func TestPluginInfoResponseValidate_NoCommands(t *testing.T) {
	r := &PluginInfoResponse{
		ABIVersion: ABIVersion, ID: "p", Name: "x", Version: "1.0.0",
	}
	if err := r.Validate(); err == nil {
		t.Fatal("expected error: at least one command")
	}
}

func TestPluginInfoResponseValidate_DuplicateCommand(t *testing.T) {
	r := &PluginInfoResponse{
		ABIVersion: ABIVersion, ID: "p", Name: "x", Version: "1.0.0",
		Commands: []CommandDecl{{Name: "签到"}, {Name: "签到"}},
	}
	if err := r.Validate(); err == nil {
		t.Fatal("expected duplicate command error")
	}
}

func TestPluginInfoResponseValidate_BadCommandName(t *testing.T) {
	for _, name := range []string{"", "/签到", "签 到", "签到\x00"} {
		r := &PluginInfoResponse{
			ABIVersion: ABIVersion, ID: "p", Name: "x", Version: "1.0.0",
			Commands: []CommandDecl{{Name: name}},
		}
		if err := r.Validate(); err == nil {
			t.Errorf("command %q should be rejected", name)
		}
	}
}

// ── HandleResponse.Validate ──

func TestHandleResponseValidate_HandledFalseNoOutputs(t *testing.T) {
	r := &HandleResponse{Handled: false}
	if err := r.Validate(&DefaultLimits); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleResponseValidate_HandledFalseWithOutputs(t *testing.T) {
	r := &HandleResponse{
		Handled: false,
		Outputs: []OutputItem{{Type: "text", Content: "hi"}},
	}
	if err := r.Validate(&DefaultLimits); err == nil {
		t.Fatal("expected error: handled=false with outputs")
	}
}

func TestHandleResponseValidate_OutputCountExceeded(t *testing.T) {
	outputs := make([]OutputItem, DefaultLimits.MaxOutputCount+1)
	for i := range outputs {
		outputs[i] = OutputItem{Type: "text", Content: "x"}
	}
	r := &HandleResponse{Handled: true, Outputs: outputs}
	if err := r.Validate(&DefaultLimits); err == nil {
		t.Fatal("expected error: too many outputs")
	}
}

func TestHandleResponseValidate_BadType(t *testing.T) {
	r := &HandleResponse{Handled: true, Outputs: []OutputItem{{Type: "image", Content: "x"}}}
	if err := r.Validate(&DefaultLimits); err == nil {
		t.Fatal("expected error: unsupported type")
	}
}

func TestHandleResponseValidate_TextTooLong(t *testing.T) {
	r := &HandleResponse{Handled: true, Outputs: []OutputItem{{Type: "text", Content: strings.Repeat("x", DefaultLimits.MaxTextLen+1)}}}
	if err := r.Validate(&DefaultLimits); err == nil {
		t.Fatal("expected error: text too long")
	}
}

// ── StateKey/Value/TTL 校验 ──

func TestValidateStateKey_Empty(t *testing.T) {
	if err := ValidateStateKey("", &DefaultLimits); err == nil {
		t.Fatal("empty key should be rejected")
	}
}

func TestValidateStateKey_TooLong(t *testing.T) {
	key := strings.Repeat("a", DefaultLimits.MaxStateKeyLen+1)
	if err := ValidateStateKey(key, &DefaultLimits); err == nil {
		t.Fatal("long key should be rejected")
	}
}

func TestValidateStateKey_PathEscape(t *testing.T) {
	for _, key := range []string{"a/b", "a/../b"} {
		if err := ValidateStateKey(key, &DefaultLimits); err == nil {
			t.Errorf("key %q should be rejected", key)
		}
	}
}

func TestValidateStateKey_ControlChar(t *testing.T) {
	if err := ValidateStateKey("a\x00b", &DefaultLimits); err == nil {
		t.Fatal("control character should be rejected")
	}
}

func TestValidateStateValue_TooLong(t *testing.T) {
	value := strings.Repeat("a", DefaultLimits.MaxStateValueLen+1)
	if err := ValidateStateValue(value, &DefaultLimits); err == nil {
		t.Fatal("long value should be rejected")
	}
}

func TestValidateTTL_Negative(t *testing.T) {
	if _, err := ValidateTTL(-1, &DefaultLimits); err == nil {
		t.Fatal("negative TTL should be rejected")
	}
}

func TestValidateTTL_TooLarge(t *testing.T) {
	if _, err := ValidateTTL(int64(DefaultLimits.MaxStateTTL/time.Millisecond)+1, &DefaultLimits); err == nil {
		t.Fatal("TTL too large should be rejected")
	}
}

func TestValidateTTL_Zero(t *testing.T) {
	d, err := ValidateTTL(0, &DefaultLimits)
	if err != nil || d != 0 {
		t.Fatalf("TTL=0 should return 0, nil; got %v, %v", d, err)
	}
}

// ── Principal 生成 ──

func TestPluginPrincipal(t *testing.T) {
	got := PluginPrincipal("signin", "abc123")
	want := "plugin::signin::abc123"
	if got != want {
		t.Errorf("PluginPrincipal = %q, want %q", got, want)
	}
}

func TestUserPrincipal(t *testing.T) {
	if got := UserPrincipal("qq", "123"); got != "user::qq::123" {
		t.Errorf("UserPrincipal(qq, 123) = %q", got)
	}
}

func TestSystemPrincipal(t *testing.T) {
	if got := SystemPrincipal("startup"); got != "system::startup" {
		t.Errorf("SystemPrincipal = %q", got)
	}
}

// ── JSON 编解码 ──

func TestUnmarshalGuestInput_LimitExceeded(t *testing.T) {
	data := make([]byte, DefaultLimits.MaxGuestInputJSON+1)
	var v interface{}
	if err := UnmarshalGuestInput(data, &v, &DefaultLimits); err == nil {
		t.Fatal("expected size limit error")
	}
}

func TestHostOK_HostErr(t *testing.T) {
	ok := HostOK(StateGetData{Found: true, Value: "v"})
	if !ok.OK || ok.Error != nil {
		t.Fatalf("HostOK wrong: %+v", ok)
	}
	err := HostErr(ErrCodePermissionDenied, "denied")
	if err.OK || err.Error == nil || err.Error.Code != ErrCodePermissionDenied {
		t.Fatalf("HostErr wrong: %+v", err)
	}
}

func TestHostCodeFrom(t *testing.T) {
	tests := []struct {
		err  error
		code string
	}{
		{ErrHostInvalidRequest, ErrCodeInvalidRequest},
		{ErrHostKeyTooLarge, ErrCodeKeyTooLarge},
		{ErrHostValueTooLarge, ErrCodeValueTooLarge},
		{ErrHostTTLOutOfRange, ErrCodeTTLOutOfRange},
		{ErrHostStateUnavailable, ErrCodeStateUnavailable},
		{ErrPermissionDenied, ErrCodePermissionDenied},
	}
	for _, tt := range tests {
		if got := HostCodeFrom(tt.err); got != tt.code {
			t.Errorf("HostCodeFrom(%v) = %q, want %q", tt.err, got, tt.code)
		}
	}
}
