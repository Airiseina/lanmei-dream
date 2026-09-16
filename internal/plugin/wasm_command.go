package plugin

import (
	"context"
	"fmt"
	"strings"

	"github.com/DaWesen/lanmei-dream/internal/command"
	"github.com/DaWesen/lanmei-dream/internal/model"
)

const wasmInstallUsage = "用法：/插件 安装 <HTTPS Wasm 直链>\n请先将 Wasm 二进制上传到 GitHub Raw、GitHub Release 等可直接下载的平台。"

type wasmInstaller interface {
	Install(ctx context.Context, actor, sourceURL string) (*model.PluginInstallation, error)
}

// NewWasmInstallCommand 创建面向 bot_owner 的远程 Wasm 安装命令（/插件 安装 <URL>）。
// 命令只负责提交安装，成功后的加载/启用仍由管理流程决定。
//
// 参数：
//   - ctx：安装请求使用的上下文
//   - installer：安装实现，通常传 *WasmManager
//
// 返回：可注册到命令系统的 Command；调用者主体取消息发送者（user:: 主体）。
func NewWasmInstallCommand(ctx context.Context, installer wasmInstaller) command.Command {
	return command.Command{
		Name:        "插件",
		Description: "从公网 HTTPS 直链安装 Wasm 插件（仅管理员）",
		Order:       150,
		Handler: func(commandCtx *command.Context) error {
			parts := strings.Fields(commandCtx.Message)
			if len(parts) != 3 || parts[1] != "安装" {
				commandCtx.Reply(wasmInstallUsage)
				return nil
			}
			installation, err := installer.Install(ctx, UserPrincipal(commandCtx.Platform, commandCtx.PlatformUserID), parts[2])
			if err != nil {
				commandCtx.Reply(fmt.Sprintf("Wasm 插件安装失败：%v", err))
				return nil
			}
			commandCtx.Reply(fmt.Sprintf(
				"Wasm 插件已安装但尚未加载：%s %s（installation_id=%s）",
				installation.Name,
				installation.Version,
				installation.ID,
			))
			return nil
		},
	}
}
