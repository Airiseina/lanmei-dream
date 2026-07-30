package plugin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/DaWesen/lanmei-dream/internal/config"
	"github.com/DaWesen/lanmei-dream/internal/database"
	"github.com/DaWesen/lanmei-dream/internal/model"
	"go.uber.org/zap"
)

// WasmManager 负责 Wasm 文件托管、安装记录和运行时生命周期。
type WasmManager struct {
	rootDir    string
	limits     RuntimeLimits
	runtime    *Runtime
	httpClient *http.Client
	authorizer Authorizer
	store      *database.PluginInstallationStore
	registry   *Registry
	logger     *zap.Logger
}

// NewWasmManager 创建插件管理器，并确保受控目录存在。
func NewWasmManager(cfg *config.PluginConfig, db *database.DB, registry *Registry, authorizer Authorizer, logger *zap.Logger, limits *RuntimeLimits) (*WasmManager, error) {
	if cfg == nil || strings.TrimSpace(cfg.RootDir) == "" {
		return nil, fmt.Errorf("插件 root_dir 不能为空")
	}
	if db == nil || db.Orm == nil {
		return nil, fmt.Errorf("插件管理器需要数据库连接")
	}
	if registry == nil || authorizer == nil {
		return nil, fmt.Errorf("插件管理器缺少 Registry 或 Authorizer")
	}
	selectedLimits := DefaultLimits
	if limits != nil {
		selectedLimits = *limits
	}
	root, err := filepath.Abs(cfg.RootDir)
	if err != nil {
		return nil, fmt.Errorf("解析插件 root_dir: %w", err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("创建插件根目录: %w", err)
	}
	return &WasmManager{
		rootDir:    root,
		limits:     selectedLimits,
		runtime:    NewRuntime(&selectedLimits, logger),
		httpClient: newRemoteWasmHTTPClient(),
		authorizer: authorizer,
		store:      database.NewPluginInstallationStore(db.Orm),
		registry:   registry,
		logger:     logger,
	}, nil
}

// Install 从公网 HTTPS 直链下载 Wasm，只创建 Enabled=false 安装记录。
func (m *WasmManager) Install(ctx context.Context, actor, sourceURL string) (*model.PluginInstallation, error) {
	if err := m.authorizer.Require(actor, ActionPluginInstall); err != nil {
		return nil, fmt.Errorf("安装插件权限校验: %w", err)
	}
	source, err := downloadRemoteWasm(ctx, m.httpClient, sourceURL, m.rootDir, m.limits.MaxWasmFileSize)
	if err != nil {
		return nil, err
	}
	defer os.Remove(source)
	hash, err := hashFile(source)
	if err != nil {
		return nil, fmt.Errorf("计算 Wasm SHA-256: %w", err)
	}
	installationID, err := newInstallationID()
	if err != nil {
		return nil, err
	}
	check, err := m.runtime.CreateCheckInstance(ctx, source, hash)
	if err != nil {
		return nil, err
	}
	mu := new(sync.Mutex)
	defer func() { _ = m.runtime.Close(context.Background(), check, mu) }()
	if err := m.runtime.CheckExports(check); err != nil {
		return nil, err
	}
	info, err := m.runtime.CallPluginInfo(ctx, check, mu)
	if err != nil {
		return nil, err
	}
	if err := m.validateMetadata(info); err != nil {
		return nil, err
	}
	if _, err := m.store.FindByPluginID(ctx, info.ID); err == nil {
		return nil, fmt.Errorf("%w: plugin_id=%s", ErrPluginConflict, info.ID)
	}

	managedDir := filepath.Join(m.rootDir, "installed", installationID)
	if err := os.MkdirAll(managedDir, 0o750); err != nil {
		return nil, fmt.Errorf("创建插件托管目录: %w", err)
	}
	managedPath := filepath.Join(managedDir, "plugin.wasm")
	if err := atomicCopy(source, managedPath, m.limits.MaxWasmFileSize); err != nil {
		_ = os.RemoveAll(managedDir)
		return nil, err
	}
	metadata, err := json.Marshal(info)
	if err != nil {
		_ = os.RemoveAll(managedDir)
		return nil, fmt.Errorf("编码插件元数据: %w", err)
	}
	installation := &model.PluginInstallation{
		ID:          installationID,
		PluginID:    info.ID,
		ABI:         info.ABIVersion,
		Name:        info.Name,
		Description: info.Description,
		Version:     info.Version,
		WasmPath:    managedPath,
		WasmSHA256:  hash,
		Config:      []byte("{}"),
		Metadata:    metadata,
		Enabled:     false,
	}
	if err := m.store.Create(ctx, installation); err != nil {
		_ = os.RemoveAll(managedDir)
		return nil, fmt.Errorf("创建插件安装记录: %w", err)
	}
	return installation, nil
}

// Load 实例化并注册一个安装记录，但不启用、不调用 start。
func (m *WasmManager) Load(ctx context.Context, actor, installationID string) error {
	if err := m.authorizer.Require(actor, ActionPluginLoad); err != nil {
		return fmt.Errorf("加载插件权限校验: %w", err)
	}
	installation, err := m.store.FindByID(ctx, installationID)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrPluginNotInstalled, installationID, err)
	}
	if _, exists := m.registry.Get(installation.PluginID); exists {
		return fmt.Errorf("%w: plugin_id=%s", ErrPluginConflict, installation.PluginID)
	}
	if err := m.verifyManagedFile(installation); err != nil {
		return err
	}

	instance, err := m.runtime.CreateProductionInstance(
		ctx,
		installation.WasmPath,
		installation.WasmSHA256,
		NewStateHostFunctions(m.authorizer, m.registry.store, PluginPrincipal(installation.PluginID, installation.ID), installation.ID, &m.limits, m.logger),
	)
	if err != nil {
		return err
	}
	plugin := NewWasmPlugin(PluginInfoResponse{
		ABIVersion:  installation.ABI,
		ID:          installation.PluginID,
		Name:        installation.Name,
		Description: installation.Description,
		Version:     installation.Version,
	}, installation.ID, m.runtime, instance, m.authorizer)
	mu := &plugin.callMu
	if err := m.runtime.CheckExports(instance); err != nil {
		_ = plugin.Close(context.Background())
		return err
	}
	info, err := m.runtime.CallPluginInfo(ctx, instance, mu)
	if err != nil {
		_ = plugin.Close(context.Background())
		return err
	}
	if info.ID != installation.PluginID || info.ABIVersion != installation.ABI || info.Version != installation.Version {
		_ = plugin.Close(context.Background())
		return fmt.Errorf("%w: 安装记录与运行时元数据不一致", ErrInvalidMetadata)
	}
	if err := m.validateMetadata(info); err != nil {
		_ = plugin.Close(context.Background())
		return err
	}
	plugin.metadata = *info
	roles, err := m.authorizer.RolesFor(plugin.Principal())
	if err != nil {
		_ = plugin.Close(context.Background())
		return err
	}
	if err := requiredRolesGranted(info.RequestedRoles, roles); err != nil {
		_ = plugin.Close(context.Background())
		return err
	}
	actions, err := m.authorizer.ActionsFor(plugin.Principal())
	if err != nil {
		_ = plugin.Close(context.Background())
		return err
	}
	pluginConfig := make(map[string]string)
	if len(installation.Config) != 0 {
		if err := json.Unmarshal(installation.Config, &pluginConfig); err != nil {
			_ = plugin.Close(context.Background())
			return fmt.Errorf("解码插件配置: %w", err)
		}
	}
	initRequest := InitRequest{
		ABIVersion:       ABIVersion,
		PluginID:         plugin.metadata.ID,
		InstallationID:   plugin.installationID,
		Config:           pluginConfig,
		GrantedRoles:     SortedStrings(roles),
		EffectiveActions: effectiveActions(actions),
	}
	if err := m.runtime.CallInit(ctx, instance, mu, initRequest); err != nil {
		_ = plugin.Close(context.Background())
		return err
	}
	if err := m.registry.Register(plugin); err != nil {
		_ = plugin.Close(context.Background())
		return fmt.Errorf("注册 Wasm 插件: %w", err)
	}
	return nil
}

// Start 启用已加载插件并调用可选 start。
func (m *WasmManager) Start(ctx context.Context, actor, installationID string) error {
	if err := m.authorizer.Require(actor, ActionPluginStart); err != nil {
		return fmt.Errorf("启动插件权限校验: %w", err)
	}
	installation, err := m.store.FindByID(ctx, installationID)
	if err != nil {
		return err
	}
	if err := m.registry.InitPlugin(ctx, installation.PluginID); err != nil {
		return err
	}
	if err := m.registry.StartPlugin(ctx, installation.PluginID); err != nil {
		return err
	}
	if err := m.store.UpdateEnabled(ctx, installationID, true, ""); err != nil {
		return fmt.Errorf("更新插件启用状态: %w", err)
	}
	return nil
}

// Unload 停止并注销插件，保留安装文件、策略和状态。
func (m *WasmManager) Unload(ctx context.Context, actor, installationID string) error {
	if err := m.authorizer.Require(actor, ActionPluginUnload); err != nil {
		return fmt.Errorf("卸载插件权限校验: %w", err)
	}
	installation, err := m.store.FindByID(ctx, installationID)
	if err != nil {
		return err
	}
	if err := m.store.UpdateEnabled(ctx, installationID, false, ""); err != nil {
		return fmt.Errorf("禁用插件: %w", err)
	}
	if plugin, ok := m.registry.Get(installation.PluginID); ok {
		if wasmPlugin, ok := plugin.(*WasmPlugin); ok {
			wasmPlugin.SetStopReason(StopReasonUnload)
		}
		if err := m.registry.Unregister(installation.PluginID); err != nil {
			return err
		}
		if wasmPlugin, ok := plugin.(*WasmPlugin); ok {
			if err := wasmPlugin.Close(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

// LoadEnabled 恢复所有 Enabled=true 安装。单插件失败不阻断其他插件。
func (m *WasmManager) LoadEnabled(ctx context.Context, actor string) error {
	if err := m.authorizer.Require(actor, ActionPluginLoad); err != nil {
		return fmt.Errorf("恢复插件权限校验: %w", err)
	}
	installations, err := m.store.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for _, installation := range installations {
		restoreErr := m.Load(ctx, actor, installation.ID)
		if restoreErr == nil {
			restoreErr = m.registry.InitPlugin(ctx, installation.PluginID)
		}
		if restoreErr == nil {
			restoreErr = m.registry.StartPlugin(ctx, installation.PluginID)
		}
		if restoreErr == nil {
			continue
		}
		if _, loaded := m.registry.Get(installation.PluginID); loaded {
			_ = m.registry.Unregister(installation.PluginID)
		}
		_ = m.store.UpdateEnabled(ctx, installation.ID, false, restoreErr.Error())
	}
	return nil
}

// Delete 删除安装记录和托管文件；调用前必须已卸载。
func (m *WasmManager) Delete(ctx context.Context, actor, installationID string) error {
	if err := m.authorizer.Require(actor, ActionPluginDelete); err != nil {
		return fmt.Errorf("删除插件权限校验: %w", err)
	}
	installation, err := m.store.FindByID(ctx, installationID)
	if err != nil {
		return err
	}
	if _, loaded := m.registry.Get(installation.PluginID); loaded {
		return fmt.Errorf("%w: 请先卸载", ErrPluginNotLoaded)
	}
	if err := m.store.Delete(ctx, installationID); err != nil {
		return fmt.Errorf("删除插件安装记录: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(m.rootDir, "installed", installationID)); err != nil {
		return fmt.Errorf("删除插件托管文件: %w", err)
	}
	return nil
}

func (m *WasmManager) verifyManagedFile(installation *model.PluginInstallation) error {
	cleanRoot := filepath.Clean(filepath.Join(m.rootDir, "installed")) + string(os.PathSeparator)
	cleanPath := filepath.Clean(installation.WasmPath)
	if !strings.HasPrefix(cleanPath, cleanRoot) {
		return fmt.Errorf("插件托管路径逃逸 managed root")
	}
	if err := validateWasmFile(cleanPath, m.limits.MaxWasmFileSize); err != nil {
		return err
	}
	hash, err := hashFile(cleanPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(hash, installation.WasmSHA256) {
		return fmt.Errorf("Wasm SHA-256 不匹配")
	}
	return nil
}

func (m *WasmManager) validateMetadata(info *PluginInfoResponse) error {
	if err := info.Validate(); err != nil {
		return err
	}
	reserved := make(map[string]bool)
	for _, command := range m.registry.cmdSys.List() {
		reserved[command.Name] = true
	}
	for _, command := range info.Commands {
		if err := ValidateCommandDecl(command, reserved); err != nil {
			return fmt.Errorf("%w: %w", ErrPluginConflict, err)
		}
	}
	for _, role := range info.RequestedRoles {
		if !m.authorizer.IsKnownRole(role.Role) {
			return fmt.Errorf("未知角色: %s", role.Role)
		}
	}
	return nil
}

func requiredRolesGranted(requests []RoleRequest, granted []string) error {
	set := make(map[string]bool, len(granted))
	for _, role := range granted {
		set[role] = true
	}
	for _, request := range requests {
		if request.Required && !set[request.Role] {
			return fmt.Errorf("%w: 缺少必需角色 %s", ErrInvalidMetadata, request.Role)
		}
	}
	return nil
}

func effectiveActions(permissions [][]string) []string {
	set := make(map[string]bool)
	for _, permission := range permissions {
		if len(permission) >= 2 {
			set[permission[1]] = true
		}
	}
	actions := make([]string, 0, len(set))
	for action := range set {
		actions = append(actions, action)
	}
	return SortedStrings(actions)
}

func validateWasmFile(path string, maxSize int64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开 Wasm 文件: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() <= 8 || info.Size() > maxSize {
		return fmt.Errorf("Wasm 文件大小无效: %d", info.Size())
	}
	var magic [8]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return fmt.Errorf("读取 Wasm magic: %w", err)
	}
	if string(magic[:4]) != "\x00asm" || magic[4] != 0x01 || magic[5] != 0x00 || magic[6] != 0x00 || magic[7] != 0x00 {
		return fmt.Errorf("Wasm magic/version 无效")
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func atomicCopy(source, destination string, maxSize int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".plugin-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := io.CopyN(temporary, input, maxSize+1); err != nil && err != io.EOF {
		_ = temporary.Close()
		return err
	}
	if info, err := temporary.Stat(); err != nil || info.Size() > maxSize {
		_ = temporary.Close()
		return fmt.Errorf("复制的 Wasm 文件超过大小限制")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("同步 Wasm 临时文件: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("原子托管 Wasm 文件: %w", err)
	}
	return nil
}

func newInstallationID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("生成 installation_id: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}
