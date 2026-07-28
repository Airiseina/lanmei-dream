package plugin

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zrurf/conduit"
)

// WasmCommandPass 将可信 Conduit 命令上下文转换为 ABI command 事件。
type WasmCommandPass struct {
	plugin *WasmPlugin
}

var _ conduit.Pass = (*WasmCommandPass)(nil)

// Execute 调用 Guest 并将文本输出映射回当前事件目标。
func (p *WasmCommandPass) Execute(ctx *conduit.MessageContext) error {
	if err := p.plugin.authorizer.Require(p.plugin.principal, ActionCommandHandle); err != nil {
		return err
	}

	commandName, ok := ctx.Extra["command_name"].(string)
	if !ok || strings.TrimSpace(commandName) == "" {
		return fmt.Errorf("可信命令名缺失")
	}

	rawArgs := rawCommandArgs(ctx.RawMsg, commandName)
	request := HandleRequest{
		ABIVersion: ABIVersion,
		EventID:    eventIDFromExtra(ctx),
		EventType:  EventTypeCommand,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Message: MessageInfo{
			MessageID: stringExtra(ctx, "message_id"),
			Text:      ctx.RawMsg,
			Raw:       ctx.RawMsg,
			User: UserInfo{
				ID:       ctx.UserID,
				Nickname: stringExtra(ctx, "nickname"),
			},
			Group:   groupInfo(ctx),
			IsGroup: ctx.IsGroup,
		},
		Command: CommandInfo{
			Name:       commandName,
			Args:       strings.Fields(rawArgs),
			RawArgs:    rawArgs,
			RawMessage: ctx.RawMsg,
		},
	}

	response, err := p.plugin.runtime.CallHandle(ctx.Ctx, p.plugin.instance, &p.plugin.callMu, request)
	if err != nil {
		return fmt.Errorf("Wasm 命令处理失败 plugin=%s installation=%s: %w", p.plugin.metadata.ID, p.plugin.installationID, err)
	}
	if !response.Handled || len(response.Outputs) == 0 {
		return nil
	}
	if err := p.plugin.authorizer.Require(p.plugin.principal, ActionMessageReply); err != nil {
		return err
	}

	for _, output := range response.Outputs {
		conduit.AppendOutput(ctx, &conduit.Message{
			UserID:  ctx.UserID,
			GroupID: ctx.GroupID,
			IsGroup: ctx.IsGroup,
			Content: output.Content,
		})
	}
	return nil
}

func rawCommandArgs(rawMessage, commandName string) string {
	trimmed := strings.TrimSpace(rawMessage)
	prefix := "/" + commandName
	if !strings.HasPrefix(trimmed, prefix) {
		return ""
	}
	rest := trimmed[len(prefix):]
	if rest != "" {
		first, _ := utf8.DecodeRuneInString(rest)
		if !unicode.IsSpace(first) {
			return ""
		}
	}
	return strings.TrimSpace(rest)
}

func stringExtra(ctx *conduit.MessageContext, key string) string {
	value, _ := ctx.Extra[key].(string)
	return value
}

func eventIDFromExtra(ctx *conduit.MessageContext) string {
	if eventID := stringExtra(ctx, "event_id"); eventID != "" {
		return eventID
	}
	if messageID := stringExtra(ctx, "message_id"); messageID != "" {
		return "msg-" + messageID
	}
	return fmt.Sprintf("evt-%d", time.Now().UTC().UnixNano())
}

func groupInfo(ctx *conduit.MessageContext) *GroupInfo {
	if !ctx.IsGroup {
		return nil
	}
	return &GroupInfo{ID: ctx.GroupID}
}
