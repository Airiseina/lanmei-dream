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

// NewWasmInstallCommand 创建面向 bot_owner 的远程 Wasm 安装命令。
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
