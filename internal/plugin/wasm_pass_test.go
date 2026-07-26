package plugin

import "testing"

func TestRawCommandArgs_NoArgs(t *testing.T) {
	got := rawCommandArgs("/签到", "签到")
	if got != "" {
		t.Errorf("rawCommandArgs = %q, want empty", got)
	}
}

func TestRawCommandArgs_WithArgs(t *testing.T) {
	got := rawCommandArgs("/签到 补签", "签到")
	if got != "补签" {
		t.Errorf("rawCommandArgs = %q, want %q", got, "补签")
	}
}

func TestRawCommandArgs_PreservesInternalSpaces(t *testing.T) {
	got := rawCommandArgs("/签到   补   签", "签到")
	if got != "补   签" {
		t.Errorf("rawCommandArgs = %q, want %q", got, "补   签")
	}
}

func TestRawCommandArgs_NoMatch(t *testing.T) {
	got := rawCommandArgs("/other", "签到")
	if got != "" {
		t.Errorf("rawCommandArgs = %q, want empty", got)
	}
}

func TestRawCommandArgs_PrefixNotCommand(t *testing.T) {
	// "/签到2" 不匹配命令 "签到"，因为 '2' 不是空白分隔符
	got := rawCommandArgs("/签到2", "签到")
	if got != "" {
		t.Errorf("rawCommandArgs = %q, want empty for /签到2", got)
	}
}
