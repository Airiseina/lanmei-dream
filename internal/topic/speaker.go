package topic

import "strings"

// SpeakerLabel 生成发言者标注「昵称(用户ID)」。
//
// 用户ID（平台账号）是稳定身份锚点：群昵称经常被修改，若上下文只标注昵称，
// 昵称一变，LLM 就会把同一人当作新成员，导致记忆串线/失效。
// 因此恒以「昵称(用户ID)」形式标注：昵称可读、ID 可锚定身份，
// 二者缺一时退化为可用的一项，均缺失时回退"用户"。
func SpeakerLabel(nickname, userID string) string {
	name := strings.TrimSpace(nickname)
	id := strings.TrimSpace(userID)
	switch {
	case name != "" && id != "":
		return name + "(" + id + ")"
	case name != "":
		return name
	case id != "":
		return id
	default:
		return "用户"
	}
}
