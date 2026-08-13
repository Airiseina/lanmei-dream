<script setup lang="ts">
// 行为树蓝图：虚幻蓝图风格可视化编辑器（主蓝图 + Sub Blueprint 子树蓝图）。
// - 树形蓝图铺满内容区，规范布局算法（父节点垂直居中于子节点块）
// - Start 起始节点（无输入引脚）；节点左入右出：从组合节点输出引脚拖线到目标输入引脚建立连接
// - 游离节点：空白右键添加的节点出现在点击位置且不自动挂树，需手动拖线连接
// - 右键交互：空白/连线/节点三类菜单；连线上右键可"在中间插入节点 / 断开"；
//   拖线到空白释放会打开"添加节点"菜单；拖动已连接线可重连
// - 节点右键菜单：属性 / 替换为 / 删除；Delete/Backspace 删除选中节点
// - 双击子树节点进入 Sub Blueprint（面包屑 + 返回上一级），子树保存走独立接口
// - 悬浮节点显示审计面板（动画 + 毛玻璃）；点击节点右侧展开属性面板
// - 序列节点输出线标注执行顺序（1/2/3…）
// - 实时执行动画：SSE 订阅 trace 流，命中管线的路径边加粗流动、节点脉冲高亮
// - 保存条仅在产生未保存变更时出现；保存需 super + step-up，写操作留审计
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import {
  ArrowLeft,
  Box,
  ChevronRight,
  Clipboard,
  Code2,
  Copy,
  Eraser,
  GitBranch,
  GitFork,
  Grid3x3,
  Link2Off,
  ListOrdered,
  Maximize2,
  Play,
  Plus,
  Redo2,
  Search,
  SlidersHorizontal,
  Trash2,
  Undo2,
  Unplug,
  Wand2,
  Workflow,
  X,
  ZoomIn,
  ZoomOut,
} from 'lucide-vue-next'
import {
  Handle,
  Position,
  VueFlow,
  type Connection,
  type Edge,
  type EdgeMouseEvent,
  type EdgeUpdateEvent,
  type Node,
  type NodeDragEvent,
  type NodeMouseEvent,
  type VueFlowStore,
} from '@vue-flow/core'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import { Codemirror } from 'vue-codemirror'
import { json } from '@codemirror/lang-json'
import { oneDark } from '@codemirror/theme-one-dark'
import { conduitApi } from '@/api'
import { openSSE } from '@/api/client'
import { useAppStore } from '@/stores/app'
import StepUpDialog from '@/components/StepUpDialog.vue'
import SaveBar from '@/components/SaveBar.vue'
import type { BTNode, ConduitSnapshot, ConduitTrace } from '@/types/api'

const app = useAppStore()

// 可添加/插入的节点类型目录（右键菜单 + 属性面板共用）
const NODE_TYPES = [
  { type: 'selector', label: '选择节点', desc: '任一子节点满足即继续（OR）', icon: Workflow },
  { type: 'sequence', label: '顺序节点', desc: '全部子节点依次执行（AND）', icon: ListOrdered },
  { type: 'condition', label: '条件节点', desc: '按注册条件分支', icon: GitBranch },
  { type: 'action', label: '动作节点', desc: '引用管线执行', icon: Play },
  { type: 'subtree', label: '子树引用', desc: '引用子树蓝图', icon: GitFork },
] as const

type EditableNodeType = (typeof NODE_TYPES)[number]['type']

// 右键菜单条目（类型 / 已有管线引用 / 已有子树引用）
interface NodeMenuItem {
  type: EditableNodeType
  label: string
  desc: string
  icon: typeof Workflow
  pipeline_id?: string
  subtree_id?: string
}

const snapshot = ref<ConduitSnapshot | null>(null)
const jsonText = ref('')    // 当前编辑内容（行为树根 或 子树根）
const loadedText = ref('')  // 当前内容的基线（用于判定 dirty）
const comment = ref('')
const mode = ref<'blueprint' | 'json'>('blueprint')

// 当前编辑上下文：主蓝图 / Sub Blueprint（子树）
const editCtx = ref<{ kind: 'tree' } | { kind: 'subtree'; id: string }>({ kind: 'tree' })
// 草稿：主蓝图与各子树的进行中内容（模式切换时保留）
const treeDraft = ref('')
const treeLoaded = ref('')
const subDrafts = new Map<string, { current: string; loaded: string }>()

// ── Vue Flow 节点/边状态 ──
const flowNodes = ref<Node[]>([])
const flowEdges = ref<Edge[]>([])
const selectedKey = ref<string>('')
// 多选集合（右键框选 / Ctrl+A），selectedKey 为其中聚焦的一个
const selectedKeys = ref(new Set<string>())
// ── 节点位置持久化（仅 manager 前端数据，不入业务 JSON）──
// 用户拖拽的位置按上下文（主蓝图 / 各子树）存于 localStorage：
// 刷新页面、删除/添加节点、切换子树后均能恢复，不再"回到算法初始位置"。
const LAYOUT_KEY = 'lanmei.bt.layout'
type LayoutMap = Record<string, { x: number; y: number }>
let layoutStore: Record<string, LayoutMap> = {}
function loadLayoutStore(): Record<string, LayoutMap> {
  try {
    const parsed = JSON.parse(localStorage.getItem(LAYOUT_KEY) ?? '{}') as unknown
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, LayoutMap>) : {}
  } catch {
    return {}
  }
}
layoutStore = loadLayoutStore()
function persistLayout() {
  try {
    localStorage.setItem(LAYOUT_KEY, JSON.stringify(layoutStore))
  } catch {
    /* localStorage 不可用（隐私模式等）时静默降级为仅内存保存 */
  }
}
// 当前编辑上下文（主蓝图 / 子树）的布局键
function layoutKey(): string {
  return editCtx.value.kind === 'tree' ? 'tree' : `subtree:${editCtx.value.id}`
}
// 当前上下文位置表（惰性创建）
function layoutForCtx(): LayoutMap {
  const k = layoutKey()
  if (!layoutStore[k]) layoutStore[k] = {}
  return layoutStore[k]
}
function getLayoutPos(key: string): { x: number; y: number } | undefined {
  return layoutForCtx()[key]
}
function setLayoutPos(key: string, pos: { x: number; y: number }) {
  layoutForCtx()[key] = { x: pos.x, y: pos.y }
  persistLayout()
}
// 清空当前上下文全部位置（工具栏"重置算法布局"）
function clearLayoutForCtx() {
  const k = layoutKey()
  if (layoutStore[k]) {
    delete layoutStore[k]
    persistLayout()
  }
}
// 游离节点：面板右键添加，尚未挂入 JSON 树（key = float-N）
const floatingNodes = ref<Record<string, { node: BTNode; pos: { x: number; y: number } }>>({})
let floatSeq = 0

// ── 编辑历史（撤销 / 重做）与剪贴板 ──
// 历史快照同时记录树 JSON、游离节点与节点位置（位置为 manager 前端数据，撤销/重做需一并恢复）
interface HistoryEntry {
  json: string
  floats: Record<string, { node: BTNode; pos: { x: number; y: number } }>
  layout: LayoutMap
}
const undoStack = ref<HistoryEntry[]>([])
const redoStack = ref<HistoryEntry[]>([])
const clipboard = ref<BTNode | null>(null)
const MAX_HISTORY = 50

// 深拷贝当前游离节点快照（修改操作前调用，保证撤销可完整恢复）
function snapshotFloats() {
  return JSON.parse(JSON.stringify(floatingNodes.value)) as Record<string, { node: BTNode; pos: { x: number; y: number } }>
}
// 深拷贝当前节点位置快照
function snapshotLayout(): LayoutMap {
  return JSON.parse(JSON.stringify(layoutForCtx())) as LayoutMap
}
function pushHistory() {
  undoStack.value.push({ json: jsonText.value, floats: snapshotFloats(), layout: snapshotLayout() })
  if (undoStack.value.length > MAX_HISTORY) undoStack.value.shift()
  redoStack.value = []
}
function resetHistory() {
  undoStack.value = []
  redoStack.value = []
}

// ── 历史栈按上下文（主蓝图 / 各子树）独立保存 ──
// 进入/退出子树时切换历史栈，而不是清空，保证切回后仍可撤销/重做。
const historyStore = new Map<string, { undo: HistoryEntry[]; redo: HistoryEntry[] }>()
// 保存当前上下文历史（须在 editCtx 变更前调用，layoutKey 仍指向旧上下文）
function saveHistoryForCtx() {
  historyStore.set(layoutKey(), { undo: undoStack.value, redo: redoStack.value })
}
// 恢复目标上下文历史（须在 editCtx 变更后调用）
function restoreHistoryForCtx() {
  const saved = historyStore.get(layoutKey())
  undoStack.value = saved?.undo ?? []
  redoStack.value = saved?.redo ?? []
}
function undo() {
  const prev = undoStack.value.pop()
  if (!prev) return
  redoStack.value.push({ json: jsonText.value, floats: snapshotFloats(), layout: snapshotLayout() })
  jsonText.value = prev.json
  floatingNodes.value = prev.floats
  layoutStore[layoutKey()] = prev.layout
  persistLayout()
  renderFlow()
}
function redo() {
  const next = redoStack.value.pop()
  if (!next) return
  undoStack.value.push({ json: jsonText.value, floats: snapshotFloats(), layout: snapshotLayout() })
  jsonText.value = next.json
  floatingNodes.value = next.floats
  layoutStore[layoutKey()] = next.layout
  persistLayout()
  renderFlow()
}

// ── 右键框选状态（右键拖动框选，支持批量操作） ──
const boxSelecting = ref(false)
const boxRect = ref<{ x: number; y: number; w: number; h: number } | null>(null)
let boxStart: { x: number; y: number } | null = null
// 右键框选结束后抑制紧随的 contextmenu（避免弹出菜单）
let suppressCtxMenuOnce = false

// 拖线释放于空白后保持的预览连接：
// 菜单打开期间在源节点与释放位置之间显示一条虚线预览边 + 锚点节点，
// 选择菜单条目后由 attachNewNode 在释放位置创建真实节点并自动连上。
const previewConnect = ref<{ source: string } | null>(null)
const previewPos = ref<{ x: number; y: number } | null>(null)

function clearPreviewConnect() {
  previewConnect.value = null
  previewPos.value = null
}

// 实时执行动画状态（SSE trace 驱动）
const executingKeys = ref(new Set<string>())
const flowingEdges = ref(new Set<string>())
let pulseTimer: number | undefined

// 是否已产生未保存变更（含游离节点：它们虽不入 JSON，但属于未落盘的草稿）
const dirty = computed(
  () => jsonText.value !== loadedText.value || Object.keys(floatingNodes.value).length > 0,
)

const isDark = computed(() => app.theme === 'dark')

// 读取当前选中的节点（树路径 / 游离节点；Start 节点无 JSON 实体）
const selectedNode = computed<BTNode | null>(() => {
  const key = selectedKey.value
  if (!key || key === 'start') return null
  if (isFloatKey(key)) return floatingNodes.value[key]?.node ?? null
  const tree = parseTree()
  if (!tree) return null
  return findNodeByKey(tree, key) ?? null
})
const isFloatSelected = computed(() => isFloatKey(selectedKey.value))

// ── JSON 解析 / 树操作 ──
function parseTree(): BTNode | null {
  try {
    const v = JSON.parse(jsonText.value)
    if (v && typeof v.type === 'string') return v as BTNode
    return null
  } catch {
    return null
  }
}

function syncJson(tree: BTNode | null) {
  jsonText.value = tree ? JSON.stringify(tree, null, 2) : ''
}

// 将多选状态同步到节点 class（高亮框选节点）；树结构变化后重新应用
// 注意：只能原地修改 class，绝不能整体替换 flowNodes 数组，
// 否则 Vue Flow 会用数组里的旧 position 重置节点 → 拖拽后的节点跳回原位置
function refreshSelectionClasses() {
  for (const n of flowNodes.value) {
    if (n.id === 'preview-target') continue
    const cls = [n.class ?? '', selectedKeys.value.has(n.id) ? 'box-sel' : ''].filter(Boolean).join(' ')
    n.class = cls || undefined
  }
}

// 键约定：根为 "0"，子节点 "0.0"、"0.1"… 首段恒为根索引
function isFloatKey(key: string): boolean {
  return key.startsWith('float-')
}

function findNodeByKey(tree: BTNode, key: string): BTNode | null {
  if (key === '0') return tree
  const parts = key.split('.').map(Number).slice(1) // 跳过根段
  let cur: BTNode = tree
  for (const p of parts) {
    if (!cur.children || p < 0 || p >= cur.children.length) return null
    cur = cur.children[p]
  }
  return cur
}

function removeNodeByKey(tree: BTNode, key: string): boolean {
  if (key === '0') return false
  const parts = key.split('.').map(Number).slice(1)
  const parentParts = parts.slice(0, -1)
  let parent: BTNode = tree
  for (const p of parentParts) {
    if (!parent.children || p < 0 || p >= parent.children.length) return false
    parent = parent.children[p]
  }
  if (!parent.children) return false
  const idx = parts[parts.length - 1]
  if (idx < 0 || idx >= parent.children.length) return false
  parent.children.splice(idx, 1)
  return true
}

function isAncestor(root: BTNode, ancestorKey: string, targetKey: string): boolean {
  if (ancestorKey === targetKey) return true
  const ancestor = findNodeByKey(root, ancestorKey)
  if (!ancestor?.children) return false
  for (let i = 0; i < ancestor.children.length; i++) {
    if (isAncestor(root, `${ancestorKey}.${i}`, targetKey)) return true
  }
  return false
}

// ── 规范树布局：父节点垂直居中于子节点块，前置 Start 起始节点 ──
function renderFlow() {
  const tree = parseTree()
  if (!tree) {
    flowNodes.value = []
    flowEdges.value = []
    return
  }
  const nodes: Node[] = []
  const edges: Edge[] = []
  const xGap = 260
  const nodeH = 86

  const subtreeHeight = (n: BTNode): number => {
    if (!n.children?.length) return 1
    return n.children.reduce((s, c) => s + subtreeHeight(c), 0)
  }

  const isExecuting = (k: string) => executingKeys.value.has(k)
  const isFlowing = (id: string) => flowingEdges.value.has(id)

  const walk = (node: BTNode, depth: number, key: string, _parentKey: string | null, topY: number) => {
    const height = subtreeHeight(node)
    const autoPos = { x: (depth + 1) * xGap, y: topY + ((height - 1) * nodeH) / 2 }
    const pos = getLayoutPos(key) ?? autoPos
    nodes.push({
      id: key,
      type: node.type,
      position: pos,
      class: isExecuting(key) ? 'executing' : '',
      data: { node, label: labelOf(node), key },
    })
    let childTop = topY
    node.children?.forEach((child, i) => {
      const ck = `${key}.${i}`
      // 组合节点（选择/顺序）为每个子节点分配独立输出引脚 out-N（N 从 0 起），
      // 动态引脚可右键增删，直观反映流程顺序
      const isDyn = node.type === 'selector' || node.type === 'sequence'
      const edgeId = `${key}-${ck}`
      edges.push({
        id: edgeId,
        source: key,
        sourceHandle: isDyn ? `out-${i}` : undefined,
        target: ck,
        label: isDyn ? String(i) : undefined,
        labelShowBg: true,
        labelStyle: { fill: 'var(--mgr-text)', fontSize: 11, fontWeight: 600 },
        labelBgStyle: { fill: 'var(--mgr-bg-card)', stroke: 'var(--mgr-border-strong)', strokeWidth: 1 },
        class: isFlowing(edgeId) ? 'flowing' : '',
      })
      walk(child, depth + 1, ck, key, childTop)
      childTop += subtreeHeight(child) * nodeH
    })
  }
  walk(tree, 0, '0', null, 0)

  // Start 起始节点（视图层概念，不入 JSON）
  const rootY = ((subtreeHeight(tree) - 1) * nodeH) / 2
  // 子树蓝图内 Start 恒用算法位置（不复用主蓝图的拖拽覆盖），主蓝图保留用户覆盖
  const startPos = editCtx.value.kind === 'subtree' ? { x: 0, y: rootY } : getLayoutPos('start') ?? { x: 0, y: rootY }
  nodes.push({
    id: 'start',
    type: 'start',
    position: startPos,
    data: { label: 'Start' },
  })
  edges.push({
    id: 'start-0',
    source: 'start',
    target: '0',
    class: isFlowing('start-0') ? 'flowing' : '',
  })

  // 游离节点（保持用户放置位置）
  for (const [fid, f] of Object.entries(floatingNodes.value)) {
    nodes.push({
      id: fid,
      type: f.node.type,
      position: f.pos,
      class: isExecuting(fid) ? 'executing' : '',
      data: { node: f.node, label: labelOf(f.node), key: fid, floating: true },
    })
  }

  // 拖线预览：源节点 → 释放位置锚点（虚线预览边，随菜单保留）
  if (previewConnect.value && previewPos.value) {
    nodes.push({
      id: 'preview-target',
      type: 'preview',
      position: previewPos.value,
      data: {},
      selectable: false,
      draggable: false,
      connectable: false,
    })
    edges.push({
      id: 'preview-connect',
      source: previewConnect.value.source,
      target: 'preview-target',
      class: 'preview-connect',
      animated: true,
    })
  }

  flowNodes.value = nodes
  flowEdges.value = edges
}

function labelOf(node: BTNode): string {
  switch (node.type) {
    case 'selector': return '选择 · 任一子节点满足即继续'
    case 'sequence': return '顺序 · 全部子节点依次执行'
    case 'condition': return node.condition || '未指定条件'
    case 'action': return node.pipeline_id || '未指定管线'
    case 'subtree': return node.subtree_id || '未指定子树'
    default: return node.name
  }
}

function nodeTypeLabel(node: BTNode): string {
  switch (node.type) {
    case 'selector': return '选择节点（OR）'
    case 'sequence': return '顺序节点（AND）'
    case 'condition': return '条件节点'
    case 'action': return '动作节点'
    case 'subtree': return '子树引用'
    default: return node.type
  }
}

function nodeHeadIcon(type: string) {
  switch (type) {
    case 'selector': return Workflow
    case 'sequence': return ListOrdered
    case 'condition': return GitBranch
    case 'action': return Play
    case 'subtree': return GitFork
    default: return Box
  }
}

// ── Vue Flow 实例（视图变换 / 缩放，@init 注入） ──
let flowStore: VueFlowStore | null = null
function onFlowInit(s: VueFlowStore) {
  flowStore = s
}
// 屏幕坐标（viewport/client） → 画布坐标（右键/拖线释放位置创建节点）
function flowProject(x: number, y: number) {
  if (flowStore?.screenToFlowCoordinate) return flowStore.screenToFlowCoordinate({ x, y })
  return flowStore?.project ? flowStore.project({ x, y }) : { x, y }
}

// ── 节点交互：点击选中 / 双击进入子树 / 右键菜单 / 拖拽微调 / 悬浮审计 ──
function onNodeClick({ node }: NodeMouseEvent) {
  if (node.id === 'preview-target') return // 预览锚点不参与选中
  selectedKey.value = node.id
  selectedKeys.value = new Set([node.id])
  refreshSelectionClasses()
}

// 双击进入子树：可接收 Vue Flow 完整事件，也可由节点 slot 内联调用（只传 id + data）
function onNodeDblClick(payload: { node: { id?: string; data?: { node?: BTNode } } }) {
  const node = payload.node.data?.node
  const subId = node?.subtree_id
  if (node?.type === 'subtree' && subId) enterSubtree(subId)
}

function onPaneClick() {
  // 拖线释放后的"公共祖先 click"不视为点击空白，避免菜单刚打开就被关闭
  if (Date.now() - lastMenuOpenAt < 250) return
  selectedKey.value = ''
  selectedKeys.value = new Set()
  refreshSelectionClasses()
  closeCtxMenu()
  hoverAudit.value = null
}

// 拖动开始：先把当前位置入历史栈（一次拖动 = 一次可撤销操作，位置随撤销/重做恢复）
let dragPushedHistory = false
function onNodeDragStart() {
  pushHistory()
  dragPushedHistory = true
}

function onNodeDragStop({ node }: NodeDragEvent) {
  if (isFloatKey(node.id)) {
    const f = floatingNodes.value[node.id]
    if (f) {
      const moved = f.pos.x !== node.position.x || f.pos.y !== node.position.y
      if (moved) f.pos = { x: node.position.x, y: node.position.y }
      else if (dragPushedHistory) undoStack.value.pop() // 未发生位移：撤销刚入栈的无用历史
    }
  } else {
    const cur = getLayoutPos(node.id)
    const moved = !cur || cur.x !== node.position.x || cur.y !== node.position.y
    if (moved) setLayoutPos(node.id, { x: node.position.x, y: node.position.y })
    else if (dragPushedHistory) undoStack.value.pop()
  }
  dragPushedHistory = false
}

function resetLayout() {
  clearLayoutForCtx()
  renderFlow()
}

// 节点悬浮审计：立即切换面板 + 缓存避免重复请求
const hoverAudit = ref<{ node: BTNode; total: number; ok: number; err: number; avgMs: number; items: ConduitTrace[] } | null>(null)
const hoverLoading = ref(false)
const hoverPos = ref({ x: 0, y: 0 })
const auditCache = new Map<string, { total: number; ok: number; err: number; avgMs: number; items: ConduitTrace[]; ts: number }>()
let hoverTimer: number | undefined

function onNodeEnter({ event, node }: NodeMouseEvent) {
  const data = node.data as { node: BTNode } | undefined
  window.clearTimeout(hoverTimer)
  hoverAudit.value = null // 立即清除旧面板（避免残留）
  if (!data?.node) return
  const ev = event as MouseEvent
  hoverPos.value = { x: Math.min(ev.clientX + 16, window.innerWidth - 330), y: Math.min(ev.clientY + 8, window.innerHeight - 320) }
  if (data.node.pipeline_id) {
    hoverTimer = window.setTimeout(() => void loadNodeAudit(data.node), 250)
  }
}

function onNodeLeave() {
  window.clearTimeout(hoverTimer)
  hoverAudit.value = null
}

// 拉取指定管线近 24h 审计（带 60s 缓存），悬浮面板与属性面板共用
async function fetchPipelineAudit(pipelineId: string) {
  const cached = auditCache.get(pipelineId)
  if (cached && Date.now() - cached.ts < 60_000) return cached
  const since = new Date(Date.now() - 24 * 3600 * 1000).toISOString()
  const res = await conduitApi.traces({ pipeline: pipelineId, since, page: 1, pageSize: 8 })
  const items = res.items ?? []
  const ok = items.filter((t) => t.status === 'ok').length
  const avgMs = items.length ? Math.round(items.reduce((s, t) => s + t.duration_ms, 0) / items.length) : 0
  const data = { total: res.total, ok, err: items.length - ok, avgMs, items, ts: Date.now() }
  auditCache.set(pipelineId, data)
  return data
}

async function loadNodeAudit(bNode: BTNode) {
  if (!bNode.pipeline_id) return
  hoverLoading.value = true
  try {
    const d = await fetchPipelineAudit(bNode.pipeline_id)
    hoverAudit.value = { node: bNode, total: d.total, ok: d.ok, err: d.err, avgMs: d.avgMs, items: d.items }
  } catch {
    hoverAudit.value = null
  } finally {
    hoverLoading.value = false
  }
}

// ── 右侧属性面板：属性 / 审计 双 Tab ──
const propsTab = ref<'props' | 'audit'>('props')
const panelAudit = ref<{ total: number; ok: number; err: number; avgMs: number; items: ConduitTrace[] } | null>(null)

async function loadPanelAudit() {
  const n = selectedNode.value
  if (!n?.pipeline_id) {
    panelAudit.value = null
    return
  }
  try {
    const d = await fetchPipelineAudit(n.pipeline_id)
    panelAudit.value = { total: d.total, ok: d.ok, err: d.err, avgMs: d.avgMs, items: d.items }
  } catch {
    panelAudit.value = null
  }
}

function switchPropsTab(t: 'props' | 'audit') {
  propsTab.value = t
  if (t === 'audit') void loadPanelAudit()
}

// 切换选中节点时：回到属性 Tab 并预取审计
watch(selectedKey, () => {
  propsTab.value = 'props'
  panelAudit.value = null
  if (selectedNode.value?.pipeline_id) void loadPanelAudit()
})

// ── 右键上下文菜单（空白 / 节点 / 连线 / 引脚拖出 / 动态输出引脚） ──
interface CtxMenuState {
  visible: boolean
  x: number
  y: number
  kind: 'pane' | 'node' | 'edge' | 'connect' | 'pin'
  edge: Edge | null
  nodeKey: string
  pendingSource: string
  search: string
  showReplace: boolean
  // 节点菜单的断连二级菜单：'outputs' 断开输出 / 'inputs' 断开输入
  subMenu: 'outputs' | 'inputs' | null
  // 动态输出引脚菜单：组合节点的某个输出引脚（含序号与总数）
  pin: { key: string; idx: number; count: number } | null
}

const ctxMenu = ref<CtxMenuState>({
  visible: false,
  x: 0,
  y: 0,
  kind: 'pane',
  edge: null,
  nodeKey: '',
  pendingSource: '',
  search: '',
  showReplace: false,
  subMenu: null,
  pin: null,
})

// 菜单打开时间戳：拖线释放后浏览器会在公共祖先上派发一次 click，
// 若紧跟菜单打开（<250ms）则忽略，避免"刚打开就被关闭"。
let lastMenuOpenAt = 0

function openCtxMenu(
  kind: 'pane' | 'node' | 'edge' | 'connect' | 'pin',
  x: number,
  y: number,
  patch: Partial<CtxMenuState> = {},
) {
  lastMenuOpenAt = Date.now()
  ctxMenu.value = {
    visible: true,
    x: Math.max(8, Math.min(x, window.innerWidth - 330)),
    y: Math.max(8, Math.min(y, window.innerHeight - 440)),
    kind,
    edge: null,
    nodeKey: '',
    pendingSource: '',
    search: '',
    showReplace: false,
    subMenu: null,
    pin: null,
    ...patch,
  }
}

function closeCtxMenu() {
  ctxMenu.value.visible = false
  // 关闭菜单同时取消拖线预览（点击空白 = 取消连接）；有预览才重渲染
  if (previewConnect.value) {
    clearPreviewConnect()
    renderFlow()
  }
}

function onPaneContextMenu(event: MouseEvent) {
  event.preventDefault()
  // 框选结束后紧随的 contextmenu 被抑制（右键拖动 ≠ 打开菜单）
  if (suppressCtxMenuOnce) {
    suppressCtxMenuOnce = false
    return
  }
  openCtxMenu('pane', event.clientX, event.clientY)
}

// ── 右键拖动框选：按下记录起点，移动超阈值进入框选，释放时按框选矩形选中节点 ──
// （Vue Flow 无 pane-mousedown 事件，在 bp-wrap 上用捕获阶段监听原生右键按下）
function onWrapMouseDown(e: MouseEvent) {
  if (e.button !== 2) return // 仅右键
  const t = e.target as HTMLElement | null
  // 节点 / 边 / 引脚 / 小地图 / 工具栏 / 菜单上的右键交给各自处理
  if (t && t.closest('.vue-flow__node, .vue-flow__edge, .vue-flow__handle, .vue-flow__minimap, .bp-toolbar, .ctx-menu')) return
  boxStart = { x: e.clientX, y: e.clientY }
  boxSelecting.value = false
  boxRect.value = null
  window.addEventListener('mousemove', onBoxMove)
  window.addEventListener('mouseup', onBoxUp)
}

function onBoxMove(e: MouseEvent) {
  if (!boxStart) return
  const dx = e.clientX - boxStart.x
  const dy = e.clientY - boxStart.y
  if (!boxSelecting.value && Math.hypot(dx, dy) > 5) boxSelecting.value = true
  if (boxSelecting.value) {
    boxRect.value = {
      x: Math.min(boxStart.x, e.clientX),
      y: Math.min(boxStart.y, e.clientY),
      w: Math.abs(dx),
      h: Math.abs(dy),
    }
  }
}

function onBoxUp(_e: MouseEvent) {
  window.removeEventListener('mousemove', onBoxMove)
  window.removeEventListener('mouseup', onBoxUp)
  const moved = boxSelecting.value
  const rect = boxRect.value
  boxStart = null
  boxSelecting.value = false
  boxRect.value = null
  if (moved && rect) {
    applyBoxSelection(rect)
    suppressCtxMenuOnce = true // 阻止随后弹出的空白右键菜单
  }
}

// 将屏幕矩形（画布坐标）内的节点全部选中
function applyBoxSelection(r: { x: number; y: number; w: number; h: number }) {
  if (!flowStore) return
  const p1 = flowStore.screenToFlowCoordinate({ x: r.x, y: r.y })
  const p2 = flowStore.screenToFlowCoordinate({ x: r.x + r.w, y: r.y + r.h })
  const x1 = Math.min(p1.x, p2.x)
  const y1 = Math.min(p1.y, p2.y)
  const x2 = Math.max(p1.x, p2.x)
  const y2 = Math.max(p1.y, p2.y)
  const hits: string[] = []
  for (const nd of flowNodes.value) {
    if (nd.id === 'preview-target' || nd.id === 'start') continue
    const px = nd.position.x
    const py = nd.position.y
    if (px >= x1 && px <= x2 && py >= y1 && py <= y2) hits.push(nd.id)
  }
  selectedKeys.value = new Set(hits)
  selectedKey.value = hits[0] ?? ''
  refreshSelectionClasses()
  if (hits.length) MessagePlugin.info(`已选中 ${hits.length} 个节点`)
}

// 批量删除选中的节点（右键框选 / Ctrl+A 多选；Start 与根节点不可删）
function removeSelectedKeys() {
  const keys = [...selectedKeys.value].filter((k) => k && k !== 'start' && k !== '0')
  if (!keys.length) return
  pushHistory() // 删除前入栈，保证可撤销
  const tree = parseTree()
  let changed = false
  // 游离节点直接移除
  for (const k of keys) {
    if (isFloatKey(k) && floatingNodes.value[k]) {
      delete floatingNodes.value[k]
      changed = true
    }
  }
  if (tree) {
    const treeKeys = keys.filter((k) => !isFloatKey(k))
    // 去掉祖先/后代重叠中的后代；按深度降序删除避免索引漂移
    const filtered = treeKeys.filter((k) => !treeKeys.some((o) => o !== k && k.startsWith(o + '.')))
    filtered.sort((a, b) => {
      const da = a.split('.').length
      const db = b.split('.').length
      return da !== db ? db - da : b.localeCompare(a)
    })
    for (const k of filtered) {
      if (removeNodeByKey(tree, k)) changed = true
    }
    if (changed) {
      syncJson(tree)
    }
  }
  if (changed) renderFlow() // 游离 / 树节点删除后统一刷新（游离节点不入 JSON，必须显式刷新才会从画布消失）
  selectedKeys.value = new Set()
  selectedKey.value = ''
  if (!changed) MessagePlugin.info('无可删除的节点')
}

// ── 动态输出引脚（虚幻蓝图 Sequence 风格：0/1/2… 可增删、可调序） ──

// 组合节点右侧引脚行右键：打开引脚菜单
function pinMenu(ev: MouseEvent, key: string, idx: number, count: number) {
  ev.preventDefault()
  ev.stopPropagation()
  selectedKey.value = key
  selectedKeys.value = new Set([key])
  refreshSelectionClasses()
  openCtxMenu('pin', ev.clientX, ev.clientY, { pin: { key, idx, count } })
}

// 添加输出引脚：组合节点尾部追加一个空条件子节点（生成新引脚 0/1/2…）
function addOutputPin(key: string) {
  const newNode: BTNode = { type: 'condition', name: '未指定条件' }
  if (isFloatKey(key)) {
    const f = floatingNodes.value[key]
    if (f && (f.node.type === 'selector' || f.node.type === 'sequence')) {
      pushHistory()
      f.node.children = f.node.children ?? []
      f.node.children.push(newNode)
      renderFlow()
      MessagePlugin.success(`已添加输出 ${f.node.children.length - 1}`)
    }
    return
  }
  const tree = parseTree()
  if (!tree) return
  const cur = findNodeByKey(tree, key)
  if (!cur || (cur.type !== 'selector' && cur.type !== 'sequence')) return
  pushHistory()
  cur.children = cur.children ?? []
  cur.children.push(newNode)
  syncJson(tree)
  MessagePlugin.success(`已添加输出 ${cur.children.length - 1}`)
}

// 删除输出引脚：对应子节点从树中摘除并保留为游离节点
function removeOutputPin() {
  const pin = ctxMenu.value.pin
  ctxMenu.value.visible = false
  if (!pin) return
  const childKey = `${pin.key}.${pin.idx}`
  // 游离组合节点的子节点直接摘除（未入 JSON，不入树）
  if (isFloatKey(pin.key)) {
    const f = floatingNodes.value[pin.key]
    if (!f?.node.children || pin.idx >= f.node.children.length) return
    pushHistory()
    f.node.children.splice(pin.idx, 1)
    renderFlow()
    MessagePlugin.success(`已删除输出 ${pin.idx}`)
    return
  }
  pushHistory() // detachAsFloating 内部不记历史，由调用方在操作前入栈
  detachAsFloating(childKey)
  MessagePlugin.success(`已删除输出 ${pin.idx}，子节点保留为游离节点`)
}

// 断开该引脚：摘除该引脚的连接（树内子节点转为游离节点保留；游离组合节点的子节点直接移除）
function disconnectPin() {
  const pin = ctxMenu.value.pin
  ctxMenu.value.visible = false
  if (!pin) return
  if (isFloatKey(pin.key)) {
    const f = floatingNodes.value[pin.key]
    if (!f?.node.children || pin.idx >= f.node.children.length) return
    pushHistory()
    f.node.children.splice(pin.idx, 1)
    renderFlow()
    MessagePlugin.success(`已断开输出 ${pin.idx}`)
    return
  }
  pushHistory() // detachAsFloating 内部不记历史，由调用方在操作前入栈
  detachAsFloating(`${pin.key}.${pin.idx}`)
  MessagePlugin.success(`已断开输出 ${pin.idx}，子节点保留为游离节点`)
}

// 调整输出引脚顺序（上移 / 下移，直接交换 children 顺序）
function moveOutputPin(dir: -1 | 1) {
  const pin = ctxMenu.value.pin
  ctxMenu.value.visible = false
  if (!pin) return
  const target = pin.idx + dir
  if (target < 0) return
  if (isFloatKey(pin.key)) {
    const f = floatingNodes.value[pin.key]
    if (!f?.node.children || target >= f.node.children.length) return
    pushHistory()
    const tmp = f.node.children[pin.idx]
    f.node.children[pin.idx] = f.node.children[target]
    f.node.children[target] = tmp
    renderFlow()
    return
  }
  const tree = parseTree()
  if (!tree) return
  const cur = findNodeByKey(tree, pin.key)
  if (!cur?.children || target >= cur.children.length) return
  pushHistory()
  const tmp = cur.children[pin.idx]
  cur.children[pin.idx] = cur.children[target]
  cur.children[target] = tmp
  syncJson(tree)
  MessagePlugin.success('已调整输出顺序')
}

function onNodeContextMenu({ event, node }: NodeMouseEvent) {
  if (node.id === 'start') return // Start 节点不可删除/替换，不弹菜单
  const ev = event as MouseEvent
  ev.preventDefault()
  selectedKey.value = node.id
  selectedKeys.value = new Set([node.id])
  refreshSelectionClasses()
  openCtxMenu('node', ev.clientX, ev.clientY, { nodeKey: node.id })
}

function onEdgeContextMenu({ event, edge }: EdgeMouseEvent) {
  const ev = event as MouseEvent
  ev.preventDefault()
  openCtxMenu('edge', ev.clientX, ev.clientY, { edge })
}

// 菜单分组：一级大分类（逻辑节点 / 已有管线 / 已有子树），二级为具体条目
interface MenuGroup {
  key: string
  label: string
  icon: typeof Workflow
  items: NodeMenuItem[]
}
// 一级分类折叠状态（key → 展开）
const menuOpen = ref<Record<string, boolean>>({ logic: true })
const searching = computed(() => ctxMenu.value.search.trim().length > 0)

function toggleMenuGroup(key: string) {
  menuOpen.value = { ...menuOpen.value, [key]: !menuOpen.value[key] }
}

// 搜索命中：类型名 / 描述 / pipeline_id / subtree_id（支持搜索 pipeline.xxx 等原始 id）
const menuGroups = computed<MenuGroup[]>(() => {
  const q = ctxMenu.value.search.trim().toLowerCase()
  const hit = (item: NodeMenuItem) =>
    !q ||
    [item.label, item.desc, item.type, item.pipeline_id ?? '', item.subtree_id ?? '']
      .join(' ')
      .toLowerCase()
      .includes(q)
  const typeItems = NODE_TYPES.map((t) => ({ type: t.type, label: t.label, desc: t.desc, icon: t.icon })).filter(hit)
  const pipeItems = (snapshot.value?.pipelines ?? []).map((p) => ({
    type: 'action' as EditableNodeType,
    label: `管线 ${p.id}`,
    desc: p.pass_ids?.length ? `${p.pass_ids.length} 段` : '动作节点',
    icon: Play,
    pipeline_id: p.id,
  })).filter(hit)
  const subItems = (snapshot.value?.subtrees ?? []).map((s) => ({
    type: 'subtree' as EditableNodeType,
    label: `子树 ${s.id}`,
    desc: '引用子树蓝图',
    icon: GitFork,
    subtree_id: s.id,
  })).filter(hit)
  return [
    { key: 'logic', label: '逻辑节点', icon: Workflow, items: typeItems },
    { key: 'pipelines', label: '已有管线', icon: Play, items: pipeItems },
    { key: 'subtrees', label: '已有子树', icon: GitFork, items: subItems },
  ].filter((g) => searching.value || g.items.length > 0)
})

// 选择菜单条目：按当前菜单类型执行不同动作
function pickMenuItem(item: NodeMenuItem) {
  const preset: Partial<BTNode> = {}
  if (item.pipeline_id) preset.pipeline_id = item.pipeline_id
  if (item.subtree_id) preset.subtree_id = item.subtree_id
  const pos = { x: ctxMenu.value.x, y: ctxMenu.value.y }
  switch (ctxMenu.value.kind) {
    case 'pane':
      createFloatingNode(item.type, preset, pos)
      break
    case 'connect':
      attachNewNode(item.type, preset, ctxMenu.value.pendingSource, pos)
      break
    case 'edge':
      insertNodeOnEdge(item.type, preset)
      break
  }
  ctxMenu.value.visible = false
  // 菜单条目已确定：取消拖线预览边（真实节点已由 attachNewNode 建好）
  if (previewConnect.value) {
    clearPreviewConnect()
    renderFlow()
  }
}

// 空白右键：创建游离节点（点击位置，不自动挂树，需手动拖线连接）
function createFloatingNode(type: EditableNodeType, preset: Partial<BTNode>, atScreen: { x: number; y: number }) {
  pushHistory() // 新增节点前入栈，撤销/重做按钮与 Ctrl+Z 立即可用
  const seq = ++floatSeq
  const id = `float-${seq}`
  const name = preset.pipeline_id ?? preset.subtree_id ?? `${type}-${seq}`
  floatingNodes.value[id] = { node: { type, name, ...preset }, pos: flowProject(atScreen.x, atScreen.y) }
  renderFlow()
  MessagePlugin.info(`已创建游离「${nodeTypeLabel({ type, name } as BTNode)}」，从组合节点输出引脚拖线连接`)
}

// 引脚拖出释放于空白：在释放位置创建节点并自动挂到源节点下
function attachNewNode(type: EditableNodeType, preset: Partial<BTNode>, sourceKey: string, atScreen: { x: number; y: number }) {
  const tree = parseTree()
  if (!tree) return
  const src = findNodeByKey(tree, sourceKey)
  if (!src || (src.type !== 'selector' && src.type !== 'sequence')) {
    MessagePlugin.warning('仅 选择/顺序 节点可从输出引脚连接新节点')
    return
  }
  pushHistory()
  src.children = src.children ?? []
  const seq = src.children.length + 1
  const node: BTNode = { type, name: preset.pipeline_id ?? preset.subtree_id ?? `${type}-${seq}`, ...preset }
  src.children.push(node)
  const newKey = `${sourceKey}.${src.children.length - 1}`
  setLayoutPos(newKey, flowProject(atScreen.x, atScreen.y))
  syncJson(tree)
  MessagePlugin.success('已连接新节点')
}

// 连线上右键 / 拖线到空白：在父子之间插入新节点（target 挂到新节点下）
function insertNodeOnEdge(type: EditableNodeType, preset: Partial<BTNode>) {
  const edge = ctxMenu.value.edge
  if (!edge) return
  const tree = parseTree()
  if (!tree) return
  const src = findNodeByKey(tree, edge.source)
  const tgt = findNodeByKey(tree, edge.target)
  if (!src || !tgt || !src.children) return
  const idx = src.children.indexOf(tgt)
  if (idx < 0) return
  pushHistory()
  const newNode: BTNode = { type, name: preset.pipeline_id ?? preset.subtree_id ?? `${type}-mid`, ...preset }
  src.children.splice(idx, 1)
  src.children.splice(idx, 0, newNode)
  newNode.children = [tgt]
  syncJson(tree)
  MessagePlugin.success('已在中间插入节点')
}

// 连线上右键：断开该连接（移除子节点关系）
function disconnectEdge() {
  const edge = ctxMenu.value.edge
  if (!edge) return
  const tree = parseTree()
  if (!tree || edge.target === '0') return
  pushHistory()
  if (!removeNodeByKey(tree, edge.target)) return
  syncJson(tree)
  ctxMenu.value.visible = false
  MessagePlugin.success('已断开连接')
}

// ── 节点右键：断开连接（输出/输入，支持精确到目标节点） ──

// 当前节点的输出/输入连接列表（基于 flowEdges 实时值；排除 Start 与拖线预览边）
const nodeDisconnectTargets = computed<{ outputs: { key: string; label: string }[]; inputs: { key: string; label: string }[] }>(() => {
  const key = selectedKey.value
  const outputs: { key: string; label: string }[] = []
  const inputs: { key: string; label: string }[] = []
  if (!key || key === 'start') return { outputs, inputs }
  for (const e of flowEdges.value) {
    if (e.target === 'preview-target') continue
    if (e.source === key && e.target !== 'start') {
      outputs.push({ key: e.target, label: nodeDisplayLabel(e.target) })
    }
    if (e.target === key && e.source !== 'start') {
      inputs.push({ key: e.source, label: nodeDisplayLabel(e.source) })
    }
  }
  return { outputs, inputs }
})

function nodeDisplayLabel(key: string): string {
  for (const nd of flowNodes.value) {
    if (nd.id !== key) continue
    const d = nd.data as { node?: BTNode; label?: string } | undefined
    if (d?.node) return labelOf(d.node)
    if (d?.label) return d.label
  }
  return key
}

// 将树中节点（含其子树）摘除并转为游离节点保留在画布（断开连接的语义：节点不删除）
function detachAsFloating(targetKey: string) {
  const tree = parseTree()
  if (!tree || targetKey === '0' || isFloatKey(targetKey)) return
  const node = findNodeByKey(tree, targetKey)
  if (!node) return
  let pos: { x: number; y: number } | undefined
  for (const nd of flowNodes.value) {
    if (nd.id === targetKey) {
      pos = nd.position
      break
    }
  }
  const fid = `float-${++floatSeq}`
  floatingNodes.value[fid] = { node, pos: pos ?? { x: 0, y: 0 } }
  if (!removeNodeByKey(tree, targetKey)) {
    delete floatingNodes.value[fid]
    return
  }
  syncJson(tree)
}

// 断开所有输出连接：全部子节点摘除为游离
function disconnectAllOutputs() {
  const key = selectedKey.value
  const tree = parseTree()
  if (!tree || isFloatKey(key) || key === 'start') return
  const cur = findNodeByKey(tree, key)
  if (!cur?.children?.length) return
  const keys = cur.children.map((_, i) => `${key}.${i}`)
  ctxMenu.value.visible = false
  pushHistory() // 一次操作只入栈一次（detachAsFloating 内部不记历史）
  // 从后往前摘除，保证前面索引不因删除而后移
  for (let i = keys.length - 1; i >= 0; i--) detachAsFloating(keys[i])
  MessagePlugin.success('已断开全部输出连接，子节点保留为游离节点')
}

// 断开指定输出连接（具体到某子节点）
function disconnectOutput(targetKey: string) {
  ctxMenu.value.visible = false
  pushHistory()
  detachAsFloating(targetKey)
  MessagePlugin.success('已断开该输出连接，节点保留为游离节点')
}

// 断开所有输入连接：本节点从父节点摘除为游离
function disconnectAllInputs() {
  const key = selectedKey.value
  if (!key || key === 'start' || key === '0' || isFloatKey(key)) return
  ctxMenu.value.visible = false
  pushHistory()
  detachAsFloating(key)
  MessagePlugin.success('已断开全部输入连接，本节点保留为游离节点')
}

// 断开指定输入连接（具体到某父节点；树结构下父节点唯一，等价于摘除本节点）
function disconnectInput(_sourceKey: string) {
  const key = selectedKey.value
  if (!key || key === 'start' || key === '0' || isFloatKey(key)) return
  ctxMenu.value.visible = false
  pushHistory()
  detachAsFloating(key)
  MessagePlugin.success('已断开该输入连接，本节点保留为游离节点')
}

// 节点右键：替换为其他类型（保留名称与连接，清空类型专属字段）
function replaceNodeType(type: EditableNodeType) {
  const key = selectedKey.value
  if (!key) return
  const tree = parseTree()
  const target = isFloatKey(key) ? floatingNodes.value[key]?.node : tree ? findNodeByKey(tree, key) : null
  if (!target) return
  pushHistory() // 类型替换会清空类型专属字段（可能移除子节点），计入历史
  if (type !== 'selector' && type !== 'sequence' && (target.children?.length ?? 0) > 0) {
    MessagePlugin.warning('新类型不支持子节点，原有子节点将被移除')
  }
  delete target.condition
  delete target.pipeline_id
  delete target.subtree_id
  target.type = type
  if (type !== 'selector' && type !== 'sequence') delete target.children
  if (isFloatKey(key)) renderFlow()
  else syncJson(tree)
  ctxMenu.value.visible = false
  MessagePlugin.success(`已替换为${nodeTypeLabel(target)}`)
}

// ── 可视化连线：拖线连接 / 游离节点挂载 / 连线重连 ──
let lastConnectSource: string | null = null
let connectedThisDrag = false

function onConnectStart(params: { nodeId?: string }) {
  lastConnectSource = params.nodeId ?? null
  connectedThisDrag = false
  // 开始拖线：收起任何悬浮菜单（含上一轮拖线遗留的预览），避免菜单残留（需求：拖动时菜单消失）
  closeCtxMenu()
}

function onConnect({ source, target }: Connection) {
  connectedThisDrag = true
  if (!source || !target || source === target) return
  // target 为游离节点 → 挂到 source（组合节点）下
  if (isFloatKey(target)) {
    if (isFloatKey(source)) {
      MessagePlugin.warning('游离节点暂不能作为父节点，请先挂载')
      return
    }
    if (attachFloatingAsChild(source, target)) MessagePlugin.success('已连接游离节点')
    return
  }
  if (isFloatKey(source)) {
    MessagePlugin.warning('请从组合节点输出引脚拖出连线')
    return
  }
  // 树内连接（父 → 子）
  const tree = parseTree()
  if (!tree) return
  const src = findNodeByKey(tree, source)
  const tgt = findNodeByKey(tree, target)
  if (!src || !tgt) return
  if (src.type !== 'selector' && src.type !== 'sequence') {
    MessagePlugin.warning('仅 选择/顺序 节点可包含子节点（连线起点须为组合节点）')
    return
  }
  if (target === '0') {
    MessagePlugin.warning('根节点不能被移动')
    return
  }
  if (isAncestor(tree, target, source)) {
    MessagePlugin.warning('不能将父节点连接为其自身的子节点（会形成环）')
    return
  }
  pushHistory()
  removeNodeByKey(tree, target)
  src.children = src.children ?? []
  if (!src.children.includes(tgt)) src.children.push(tgt)
  syncJson(tree)
  MessagePlugin.success('已建立连接')
}

// 引脚拖出后在空白释放：打开"添加节点"菜单，并保留源节点 → 释放位置的预览边
// （选择菜单条目后自动创建节点并连上；点击空白关闭菜单即取消连接）
function onConnectEnd(ev?: MouseEvent) {
  const made = connectedThisDrag
  connectedThisDrag = false
  const src = lastConnectSource
  lastConnectSource = null
  if (made || !src) return
  const x = ev?.clientX ?? 0
  const y = ev?.clientY ?? 0
  if (!x && !y) return
  previewConnect.value = { source: src }
  previewPos.value = flowProject(x, y)
  openCtxMenu('connect', x, y, { pendingSource: src })
  renderFlow()
}

function attachFloatingAsChild(sourceKey: string, floatId: string): boolean {
  const tree = parseTree()
  if (!tree) return false
  const src = findNodeByKey(tree, sourceKey)
  if (!src || (src.type !== 'selector' && src.type !== 'sequence')) return false
  const f = floatingNodes.value[floatId]
  if (!f) return false
  pushHistory()
  src.children = src.children ?? []
  src.children.push(f.node)
  const newKey = `${sourceKey}.${src.children.length - 1}`
  setLayoutPos(newKey, f.pos)
  delete floatingNodes.value[floatId]
  syncJson(tree)
  return true
}

// ── 连线拖动重连（edges-updatable） ──
let draggingEdge: Edge | null = null
let lastReconnect: Connection | null = null

function onEdgeUpdateStart({ edge }: EdgeMouseEvent) {
  draggingEdge = edge
  lastReconnect = null
  // 开始拖边：收起任何悬浮菜单（需求：拖动时菜单消失）
  closeCtxMenu()
}

function onEdgeUpdate({ connection }: EdgeUpdateEvent) {
  if (connection?.source && connection?.target && connection.source !== connection.target) {
    lastReconnect = connection
  }
}

// 拖动已连接线：落点有效则重连；释放于空白则打开边菜单（可"在中间插入节点"）
function onEdgeUpdateEnd({ edge: _edge, event }: EdgeMouseEvent) {
  const orig = draggingEdge
  draggingEdge = null
  const conn = lastReconnect
  lastReconnect = null
  const ev = event as MouseEvent | undefined
  if (orig && conn && (conn.source !== orig.source || conn.target !== orig.target)) {
    reconnectEdge(orig, conn)
    return
  }
  if (orig && ev) openCtxMenu('edge', ev.clientX, ev.clientY, { edge: orig })
}

function reconnectEdge(_orig: Edge, conn: Connection) {
  const tree = parseTree()
  if (!tree) return
  const src = findNodeByKey(tree, conn.source)
  const tgt = findNodeByKey(tree, conn.target)
  if (!src || !tgt || conn.source === conn.target) return
  if (conn.target === '0') {
    MessagePlugin.warning('根节点不能被移动')
    return
  }
  if (src.type !== 'selector' && src.type !== 'sequence') {
    MessagePlugin.warning('仅 选择/顺序 节点可作为父节点')
    return
  }
  if (isAncestor(tree, conn.target, conn.source)) {
    MessagePlugin.warning('不能形成环')
    return
  }
  pushHistory()
  removeNodeByKey(tree, conn.target)
  src.children = src.children ?? []
  if (!src.children.includes(tgt)) src.children.push(tgt)
  syncJson(tree)
  MessagePlugin.success('已重连')
}

// ── Sub Blueprint：进入/退出子树编辑（面包屑 + 草稿保留） ──
function enterSubtree(id: string) {
  const sub = snapshot.value?.subtrees.find((s) => s.id === id)
  if (!sub?.node) {
    MessagePlugin.warning('子树不存在')
    return
  }
  saveHistoryForCtx() // 保存当前（主蓝图）历史，切换后恢复子树自己的历史
  treeDraft.value = jsonText.value
  treeLoaded.value = loadedText.value
  const draft = subDrafts.get(id)
  const content = draft ? draft.current : JSON.stringify(sub.node, null, 2)
  const baseline = draft ? draft.loaded : content
  jsonText.value = content
  loadedText.value = baseline
  editCtx.value = { kind: 'subtree', id }
  selectedKey.value = ''
  selectedKeys.value = new Set()
  restoreHistoryForCtx()
  closeCtxMenu()
  clearFloating(true)
  hoverAudit.value = null
  void nextTick(() => void flowStore?.fitView({ padding: 0.15 }))
}

function exitSubtree() {
  if (editCtx.value.kind !== 'subtree') return
  saveHistoryForCtx() // 保存子树历史，返回主蓝图后恢复主蓝图历史
  subDrafts.set(editCtx.value.id, { current: jsonText.value, loaded: loadedText.value })
  jsonText.value = treeDraft.value
  loadedText.value = treeLoaded.value
  editCtx.value = { kind: 'tree' }
  selectedKey.value = ''
  selectedKeys.value = new Set()
  restoreHistoryForCtx()
  closeCtxMenu()
  clearFloating(true)
  hoverAudit.value = null
  void nextTick(() => void flowStore?.fitView({ padding: 0.15 }))
}

// ── 属性编辑 ──
function updateSelected(patch: Partial<BTNode>) {
  const key = selectedKey.value
  if (!key) return
  if (isFloatKey(key)) {
    const f = floatingNodes.value[key]
    if (!f) return
    Object.assign(f.node, patch)
    renderFlow()
    return
  }
  const tree = parseTree()
  if (!tree) return
  const cur = findNodeByKey(tree, key)
  if (!cur) return
  Object.assign(cur, patch)
  syncJson(tree)
}

function addChild(type: EditableNodeType) {
  if (isFloatSelected.value) {
    MessagePlugin.info('游离节点请先连接后再添加子节点')
    return
  }
  const tree = parseTree()
  if (!tree || !selectedKey.value) return
  const cur = findNodeByKey(tree, selectedKey.value)
  if (!cur) return
  if (cur.type !== 'selector' && cur.type !== 'sequence') {
    MessagePlugin.warning('仅 选择/顺序 节点可添加子节点')
    return
  }
  pushHistory()
  cur.children = cur.children ?? []
  cur.children.push({ type, name: `${type}-${cur.children.length + 1}` })
  syncJson(tree)
}

function removeSelected() {
  const key = selectedKey.value
  if (!key) return
  pushHistory() // 删除前入栈，保证可撤销
  selectedKeys.value = new Set()
  if (isFloatKey(key)) {
    delete floatingNodes.value[key]
    selectedKey.value = ''
    renderFlow()
    return
  }
  const tree = parseTree()
  if (!tree) return
  if (!removeNodeByKey(tree, key)) {
    MessagePlugin.info('根节点不可删除')
    return
  }
  selectedKey.value = ''
  syncJson(tree)
}

// ── 复制 / 粘贴 / 全选 ──
function copySelected() {
  const key = selectedKey.value
  if (!key || key === 'start') {
    MessagePlugin.info('未选中可复制的节点')
    return
  }
  let node: BTNode | null = null
  if (isFloatKey(key)) {
    const f = floatingNodes.value[key]
    if (f) node = JSON.parse(JSON.stringify(f.node)) as BTNode
  } else {
    const tree = parseTree()
    if (tree) node = JSON.parse(JSON.stringify(findNodeByKey(tree, key))) as BTNode
  }
  if (node) {
    clipboard.value = node
    MessagePlugin.success('已复制节点（Ctrl+V 粘贴）')
  }
}

function pasteClipboard() {
  const src = clipboard.value
  if (!src) return
  const node = JSON.parse(JSON.stringify(src)) as BTNode
  // 若当前选中树内组合节点 → 作为其新子节点（自动生成新输出引脚）
  const sel = selectedKey.value
  if (sel && !isFloatKey(sel) && sel !== 'start') {
    const tree = parseTree()
    if (!tree) return
    const cur = findNodeByKey(tree, sel)
    if (cur && (cur.type === 'selector' || cur.type === 'sequence')) {
      pushHistory()
      cur.children = cur.children ?? []
      cur.children.push(node)
      syncJson(tree)
      MessagePlugin.success('已粘贴为子节点')
      return
    }
  }
  // 否则粘贴为游离节点（位于视图中心附近）
  pushHistory()
  const center = flowStore?.screenToFlowCoordinate({ x: window.innerWidth / 2, y: window.innerHeight / 2 })
  const fid = `float-${++floatSeq}`
  floatingNodes.value[fid] = { node, pos: center ?? { x: 400, y: 300 } }
  renderFlow()
  MessagePlugin.success('已粘贴为游离节点（可拖线连接）')
}

function selectAllNodes() {
  const ids: string[] = []
  for (const n of flowNodes.value) {
    if (n.id === 'preview-target' || n.id === 'start') continue
    ids.push(n.id)
  }
  selectedKeys.value = new Set(ids)
  selectedKey.value = ids[0] ?? ''
  refreshSelectionClasses()
  if (ids.length) MessagePlugin.info(`已全选 ${ids.length} 个节点`)
}

// Delete / Backspace 删除选中节点、Ctrl 快捷键（输入框内不拦截）
function onWindowKeydown(e: KeyboardEvent) {
  const t = e.target as HTMLElement | null
  if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return
  const ctrl = e.ctrlKey || e.metaKey
  const k = e.key.toLowerCase()
  // 撤销 / 重做
  if (ctrl && k === 'z') {
    e.preventDefault()
    if (e.shiftKey) redo()
    else undo()
    return
  }
  if (ctrl && k === 'y') {
    e.preventDefault()
    redo()
    return
  }
  // 复制 / 粘贴
  if (ctrl && k === 'c') {
    e.preventDefault()
    copySelected()
    return
  }
  if (ctrl && k === 'v') {
    e.preventDefault()
    pasteClipboard()
    return
  }
  // 全选
  if (ctrl && k === 'a') {
    e.preventDefault()
    selectAllNodes()
    return
  }
  // 保存（提示走底部悬浮保存条，避免快捷键静默）
  if (ctrl && k === 's') {
    e.preventDefault()
    if (dirty.value) MessagePlugin.info('请点击底部"应用变更"悬浮条保存')
    return
  }
  // 删除（单/多选）
  if (e.key === 'Delete' || e.key === 'Backspace') {
    if (!selectedKeys.value.size) return
    e.preventDefault()
    removeSelectedKeys()
  }
}

// ── 实时执行动画：SSE 订阅 trace，命中管线的路径高亮 ──
let closeTrace: (() => void) | null = null
let traceDisposed = false
let traceRetry = 0
let traceReconnectTimer: number | undefined

function startTraceStream() {
  if (traceDisposed) return
  closeTrace = openSSE<ConduitTrace>(
    '/api/conduit/traces/stream',
    (name, data) => {
      traceRetry = 0 // 收到数据后重置退避
      if (name === 'trace' && data?.pipeline) pulsePipeline(data.pipeline)
    },
    () => {
      // 连接中断（容器重启 / 网络抖动 / 服务端关闭）→ 指数退避重连
      if (traceDisposed) return
      window.clearTimeout(traceReconnectTimer)
      const delay = Math.min(1000 * 2 ** traceRetry, 15000)
      traceRetry += 1
      traceReconnectTimer = window.setTimeout(startTraceStream, delay)
    },
  )
}

function pulsePipeline(pipelineId: string) {
  const tree = parseTree()
  if (!tree) return
  const keys: string[] = []
  collectActionKeys(tree, '0', pipelineId, keys)
  if (!keys.length) return
  const before = executingKeys.value.size + flowingEdges.value.size
  flowingEdges.value.add('start-0')
  for (const k of keys) {
    const segs = k.split('.')
    let cur = ''
    for (let i = 0; i < segs.length; i++) {
      const next = cur ? `${cur}.${segs[i]}` : segs[i]
      if (cur) flowingEdges.value.add(`${cur}-${next}`)
      executingKeys.value.add(next)
      cur = next
    }
  }
  if (executingKeys.value.size + flowingEdges.value.size !== before) renderFlow()
  window.clearTimeout(pulseTimer)
  pulseTimer = window.setTimeout(() => {
    executingKeys.value.clear()
    flowingEdges.value.clear()
    renderFlow()
  }, 3200)
}

function collectActionKeys(n: BTNode, key: string, pid: string, out: string[]) {
  if (n.type === 'action' && n.pipeline_id === pid) out.push(key)
  n.children?.forEach((c, i) => collectActionKeys(c, `${key}.${i}`, pid, out))
}

// ── 工具栏：缩放 / 网格 / 清除游离节点 ──
const showGrid = ref(true)

function zoomIn() {
  void flowStore?.zoomIn()
}
function zoomOut() {
  void flowStore?.zoomOut()
}
function fitView() {
  void flowStore?.fitView({ padding: 0.15 })
}
function clearFloating(silent = false) {
  const n = Object.keys(floatingNodes.value).length
  floatingNodes.value = {}
  renderFlow()
  if (n && !silent) MessagePlugin.info(`已清除 ${n} 个游离节点`)
}

// 工具栏"清除游离节点"：一次性操作需记录历史（load / 切换子树内部调用不记历史）
function clearFloatingToolbar() {
  if (!Object.keys(floatingNodes.value).length) return
  pushHistory()
  clearFloating()
}

// ── 加载与保存 ──
async function load() {
  try {
    snapshot.value = await conduitApi.snapshot()
    const ctx = editCtx.value
    if (ctx.kind === 'tree') {
      const t = JSON.stringify(snapshot.value?.behavior_tree ?? null, null, 2)
      jsonText.value = t
      loadedText.value = t
      treeDraft.value = t
      treeLoaded.value = t
    } else {
      const sub = snapshot.value?.subtrees.find((s) => s.id === ctx.id)
      const t = JSON.stringify(sub?.node ?? null, null, 2)
      jsonText.value = t
      loadedText.value = t
      subDrafts.set(ctx.id, { current: t, loaded: t })
    }
    selectedKey.value = ''
    selectedKeys.value = new Set()
    resetHistory()
    historyStore.clear() // 全新加载：各上下文历史一并作废
    clearFloating(true)
    hoverAudit.value = null
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  }
}

watch(jsonText, () => {
  if (parseTree()) {
    renderFlow()
  }
})

// 蓝图 / JSON 模式切换（模板内比较会被 v-if 收窄，统一走函数）
function setMode(m: 'blueprint' | 'json') {
  mode.value = m
}
function isMode(m: 'blueprint' | 'json') {
  return mode.value === m
}

// ── 保存：悬浮条触发 → step-up 二次验证后应用（区分主树 / 子树） ──
function onSaveRequested(c: string) {
  if (!parseTree()) {
    MessagePlugin.warning('行为树 JSON 非法，无法保存')
    return
  }
  comment.value = c
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    if (editCtx.value.kind === 'tree') {
      await conduitApi.applyBehaviorTree(parseTree(), comment.value, token)
    } else {
      await conduitApi.applySubtrees([{ id: editCtx.value.id, node: parseTree() }], comment.value, token)
    }
    MessagePlugin.success('已应用')
    await load()
  }
}

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

// JSON DSL 编辑器：Light 用默认亮色主题；Dark 用 oneDark 高质量暗色方案（行号/高亮/选中均配套）
const codemirrorExtensions = computed(() => [json(), ...(isDark.value ? [oneDark] : [])])

function onWindowClick() {
  // 与 onPaneClick 同理：忽略菜单打开后紧随的"公共祖先 click"
  if (Date.now() - lastMenuOpenAt < 250) return
  closeCtxMenu()
}

// 丢弃未保存变更，恢复基线内容（游离节点同属草稿，一并清除）
function discardChanges() {
  jsonText.value = loadedText.value
  if (Object.keys(floatingNodes.value).length) {
    floatingNodes.value = {}
    renderFlow()
  }
}

// 悬浮面板时间：HH:mm:ss
function fmtTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '-'
  const p = (n: number) => n.toString().padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// 菜单条目唯一键（同类型管线/子树可重名）
function menuKey(item: NodeMenuItem): string {
  return `${item.type}:${item.pipeline_id ?? item.subtree_id ?? item.label}`
}

onMounted(() => {
  window.addEventListener('click', onWindowClick)
  window.addEventListener('keydown', onWindowKeydown)
  load()
  traceDisposed = false
  startTraceStream()
})
onBeforeUnmount(() => {
  traceDisposed = true
  window.removeEventListener('click', onWindowClick)
  window.removeEventListener('keydown', onWindowKeydown)
  window.clearTimeout(hoverTimer)
  window.clearTimeout(pulseTimer)
  window.clearTimeout(traceReconnectTimer)
  closeTrace?.()
  closeTrace = null
})
</script>

<template>
  <div class="bt-view" :class="{ 'is-dark': isDark }">
    <!-- ═══════════ 蓝图模式 ═══════════ -->
    <div
      v-if="mode === 'blueprint'"
      class="bp-wrap"
      :class="{ 'no-grid': !showGrid }"
      @mousedown.capture="onWrapMouseDown"
    >
      <VueFlow
        :nodes="flowNodes"
        :edges="flowEdges"
        :edges-updatable="'target'"
        :delete-key-code="null"
        :fit-view-on-init="false"
        :min-zoom="0.2"
        :max-zoom="2"
        :zoom-on-double-click="false"
        @init="onFlowInit"
        @node-click="onNodeClick"
        @node-double-click="onNodeDblClick"
        @node-context-menu="onNodeContextMenu"
        @node-drag-start="onNodeDragStart"
        @node-drag-stop="onNodeDragStop"
        @node-mouse-enter="onNodeEnter"
        @node-mouse-leave="onNodeLeave"
        @pane-click="onPaneClick"
        @pane-context-menu="onPaneContextMenu"
        @edge-context-menu="onEdgeContextMenu"
        @edge-update-start="onEdgeUpdateStart"
        @edge-update="onEdgeUpdate"
        @edge-update-end="onEdgeUpdateEnd"
        @connect-start="onConnectStart"
        @connect="onConnect"
        @connect-end="onConnectEnd"
      >
        <!-- 选择节点：动态输出引脚（0/1/2…，右键引脚可删除/调序，底部添加输出） -->
        <template #node-selector="sp">
          <div class="ue-node ue-dyn">
            <div class="ue-head sel">
              <Workflow :size="13" />
              <span>选择</span>
              <span v-if="sp.data.floating" class="float-badge">游离</span>
            </div>
            <div class="ue-label">{{ sp.data.label }}</div>
            <div class="ue-outs">
              <div
                v-for="(child, i) in (sp.data.node.children ?? [])"
                :key="i"
                class="ue-out-row"
                @contextmenu.prevent.stop="pinMenu($event, sp.data.key, Number(i), (sp.data.node.children ?? []).length)"
              >
                <span class="ue-out-name">{{ labelOf(child) }}</span>
                <span class="ue-out-idx">{{ i }}</span>
                <Handle type="source" :position="Position.Right" :id="`out-${i}`" class="ue-out-handle" />
              </div>
              <button class="ue-add-out" @click.stop="addOutputPin(sp.data.key)">
                <Plus :size="12" /> 添加输出
              </button>
            </div>
            <Handle type="target" :position="Position.Left" />
          </div>
        </template>

        <!-- 顺序节点：动态输出引脚（同选择节点，执行顺序 0/1/2…） -->
        <template #node-sequence="sp">
          <div class="ue-node ue-dyn">
            <div class="ue-head seq">
              <ListOrdered :size="13" />
              <span>顺序</span>
              <span v-if="sp.data.floating" class="float-badge">游离</span>
            </div>
            <div class="ue-label">{{ sp.data.label }}</div>
            <div class="ue-outs">
              <div
                v-for="(child, i) in (sp.data.node.children ?? [])"
                :key="i"
                class="ue-out-row"
                @contextmenu.prevent.stop="pinMenu($event, sp.data.key, Number(i), (sp.data.node.children ?? []).length)"
              >
                <span class="ue-out-name">{{ labelOf(child) }}</span>
                <span class="ue-out-idx">{{ i }}</span>
                <Handle type="source" :position="Position.Right" :id="`out-${i}`" class="ue-out-handle" />
              </div>
              <button class="ue-add-out" @click.stop="addOutputPin(sp.data.key)">
                <Plus :size="12" /> 添加输出
              </button>
            </div>
            <Handle type="target" :position="Position.Left" />
          </div>
        </template>

        <!-- 条件节点：仅输入引脚 -->
        <template #node-condition="sp">
          <div class="ue-node">
            <div class="ue-head cond">
              <GitBranch :size="13" />
              <span>条件</span>
              <span v-if="sp.data.floating" class="float-badge">游离</span>
            </div>
            <div class="ue-label">{{ sp.data.label }}</div>
            <Handle type="target" :position="Position.Left" />
          </div>
        </template>

        <!-- 动作节点：仅输入引脚 -->
        <template #node-action="sp">
          <div class="ue-node">
            <div class="ue-head act">
              <Play :size="13" />
              <span>动作</span>
              <span v-if="sp.data.floating" class="float-badge">游离</span>
            </div>
            <div class="ue-label">{{ sp.data.label }}</div>
            <Handle type="target" :position="Position.Left" />
          </div>
        </template>

        <!-- 子树引用：仅输入引脚，双击进入 -->
        <template #node-subtree="sp">
          <div class="ue-node">
            <div class="ue-head sub">
              <GitFork :size="13" />
              <span>子树</span>
              <span v-if="sp.data.floating" class="float-badge">游离</span>
            </div>
            <div class="ue-label">{{ sp.data.label }}</div>
            <Handle type="target" :position="Position.Left" />
          </div>
        </template>

        <!-- Start 起始节点：仅输出引脚 -->
        <template #node-start>
          <div class="ue-start">
            <Play :size="14" />
            <span>Start</span>
          </div>
          <Handle type="source" :position="Position.Right" />
        </template>

        <!-- 拖线预览锚点（菜单打开期间占位） -->
        <template #node-preview>
          <div class="ue-preview-anchor" />
        </template>
      </VueFlow>

      <!-- 右键拖动框选矩形 -->
      <div
        v-if="boxSelecting && boxRect"
        class="box-rect"
        :style="{ left: boxRect.x + 'px', top: boxRect.y + 'px', width: boxRect.w + 'px', height: boxRect.h + 'px' }"
      />

      <!-- 面包屑 + 返回上一级 -->
      <div class="bp-breadcrumb">
        <button class="crumb" :class="{ on: editCtx.kind === 'tree' }" @click="editCtx.kind === 'subtree' && exitSubtree()">
          蓝图
        </button>
        <template v-if="editCtx.kind === 'subtree'">
          <ChevronRight :size="13" class="crumb-sep" />
          <span class="crumb on">子树 {{ editCtx.id }}</span>
          <button class="crumb-back" title="返回上一级" @click="exitSubtree()">
            <Undo2 :size="14" />
          </button>
        </template>
      </div>

      <!-- 悬浮工具栏（右上角） -->
      <div class="bp-toolbar">
        <div class="tool-seg">
          <button :class="{ on: isMode('blueprint') }" @click="setMode('blueprint')">蓝图</button>
          <button :class="{ on: isMode('json') }" @click="setMode('json')">JSON</button>
        </div>
        <div class="tool-sep" />
        <button class="tool-btn" title="撤销 (Ctrl+Z)" :disabled="!undoStack.length" @click="undo"><Undo2 :size="16" /></button>
        <button class="tool-btn" title="重做 (Ctrl+Shift+Z / Ctrl+Y)" :disabled="!redoStack.length" @click="redo"><Redo2 :size="16" /></button>
        <button class="tool-btn" title="复制节点 (Ctrl+C)" @click="copySelected"><Copy :size="16" /></button>
        <button class="tool-btn" title="粘贴节点 (Ctrl+V)" @click="pasteClipboard"><Clipboard :size="16" /></button>
        <div class="tool-sep" />
        <button class="tool-btn" title="放大" @click="zoomIn"><ZoomIn :size="16" /></button>
        <button class="tool-btn" title="缩小" @click="zoomOut"><ZoomOut :size="16" /></button>
        <button class="tool-btn" title="适应视图" @click="fitView"><Maximize2 :size="16" /></button>
        <button class="tool-btn" :class="{ on: showGrid }" title="切换网格" @click="showGrid = !showGrid"><Grid3x3 :size="16" /></button>
        <div class="tool-sep" />
        <button class="tool-btn" title="重置算法布局" @click="resetLayout"><Wand2 :size="16" /></button>
        <button class="tool-btn" title="清除游离节点" @click="clearFloatingToolbar"><Eraser :size="16" /></button>
        <transition name="live">
          <div v-if="executingKeys.size > 0" class="live-tag">
            <span class="live-dot" /> 执行中
          </div>
        </transition>
      </div>

      <!-- 悬浮审计面板 -->
      <transition name="audit">
        <div v-if="hoverAudit" class="hover-audit" :style="{ left: hoverPos.x + 'px', top: hoverPos.y + 'px' }">
          <div class="audit-head">
            <span class="audit-title">{{ hoverAudit.node.pipeline_id }}</span>
            <span class="audit-close" @click="hoverAudit = null"><X :size="13" /></span>
          </div>
          <div class="audit-stats">
            <div class="stat"><b>{{ hoverAudit.total }}</b><span>总数</span></div>
            <div class="stat ok"><b>{{ hoverAudit.ok }}</b><span>成功</span></div>
            <div class="stat err"><b>{{ hoverAudit.err }}</b><span>失败</span></div>
            <div class="stat"><b>{{ hoverAudit.avgMs }}</b><span>均耗 ms</span></div>
          </div>
          <div class="audit-list">
            <div v-for="t in hoverAudit.items.slice(0, 5)" :key="t.id" class="audit-item">
              <span class="dot" :class="t.status" />
              <span class="audit-time">{{ fmtTime(t.created_at) }}</span>
              <span class="audit-dur">{{ t.duration_ms }}ms</span>
              <span class="audit-st">{{ t.status }}</span>
            </div>
            <div v-if="hoverAudit.items.length === 0" class="audit-empty">近 24h 无执行记录</div>
          </div>
        </div>
      </transition>

      <!-- 右键菜单（空白 / 节点 / 连线 / 引脚拖出） -->
      <transition name="menu">
        <div
          v-if="ctxMenu.visible"
          class="ctx-menu"
          :style="{ left: ctxMenu.x + 'px', top: ctxMenu.y + 'px' }"
          @click.stop
        >
          <div class="ctx-head">
            <template v-if="ctxMenu.kind === 'pane'">添加节点</template>
            <template v-else-if="ctxMenu.kind === 'connect'">添加节点</template>
            <template v-else-if="ctxMenu.kind === 'edge'">连线操作</template>
            <template v-else-if="ctxMenu.kind === 'pin'">输出引脚 {{ ctxMenu.pin?.idx }}</template>
            <template v-else>节点操作</template>
          </div>

          <!-- 节点菜单：属性 / 替换为 / 断开连接 / 删除 -->
          <template v-if="ctxMenu.kind === 'node'">
            <!-- 二级：断开输出连接（精确到目标节点） -->
            <template v-if="ctxMenu.subMenu === 'outputs'">
              <div class="ctx-subtitle">断开输出连接</div>
              <button v-for="t in nodeDisconnectTargets.outputs" :key="t.key" class="ctx-item" @click="disconnectOutput(t.key)">
                <span class="ctx-dot" />
                <span class="ctx-main">{{ t.label }}</span>
                <span class="ctx-desc">{{ t.key }}</span>
              </button>
              <button class="ctx-item back" @click="ctxMenu.subMenu = null">← 返回</button>
            </template>
            <!-- 二级：断开输入连接（精确到源节点） -->
            <template v-else-if="ctxMenu.subMenu === 'inputs'">
              <div class="ctx-subtitle">断开输入连接</div>
              <button v-for="t in nodeDisconnectTargets.inputs" :key="t.key" class="ctx-item" @click="disconnectInput(t.key)">
                <span class="ctx-dot" />
                <span class="ctx-main">{{ t.label }}</span>
                <span class="ctx-desc">{{ t.key }}</span>
              </button>
              <button class="ctx-item back" @click="ctxMenu.subMenu = null">← 返回</button>
            </template>
            <!-- 替换为二级菜单 -->
            <template v-else-if="ctxMenu.showReplace">
              <div class="ctx-subtitle">替换为</div>
              <button v-for="t in NODE_TYPES" :key="t.type" class="ctx-item" @click="replaceNodeType(t.type)">
                <component :is="t.icon" />
                <span class="ctx-main">{{ t.label }}</span>
                <span class="ctx-desc">{{ t.desc }}</span>
              </button>
              <button class="ctx-item back" @click="ctxMenu.showReplace = false">← 返回</button>
            </template>
            <!-- 主菜单 -->
            <template v-else>
              <button class="ctx-item" @click="ctxMenu.showReplace = true">
                <Wand2 />
                <span>替换为…</span>
              </button>
              <template v-if="nodeDisconnectTargets.inputs.length">
                <button class="ctx-item" @click="ctxMenu.subMenu = 'inputs'">
                  <Link2Off />
                  <span class="ctx-main">断开输入连接</span>
                  <ChevronRight class="ctx-caret" />
                </button>
              </template>
              <template v-if="nodeDisconnectTargets.outputs.length">
                <button class="ctx-item" @click="ctxMenu.subMenu = 'outputs'">
                  <Link2Off />
                  <span class="ctx-main">断开输出连接</span>
                  <ChevronRight class="ctx-caret" />
                </button>
              </template>
              <template v-if="nodeDisconnectTargets.inputs.length">
                <button class="ctx-item danger" @click="disconnectAllInputs()">
                  <Unplug />
                  <span>断开所有输入连接</span>
                </button>
              </template>
              <template v-if="nodeDisconnectTargets.outputs.length">
                <button class="ctx-item danger" @click="disconnectAllOutputs()">
                  <Unplug />
                  <span>断开所有输出连接</span>
                </button>
              </template>
              <button class="ctx-item" @click="ctxMenu.visible = false">
                <SlidersHorizontal />
                <span>属性</span>
              </button>
              <button class="ctx-item danger" @click="removeSelected(); ctxMenu.visible = false">
                <Trash2 />
                <span>删除</span>
              </button>
            </template>
          </template>

          <!-- 引脚菜单：断开该引脚 / 删除输出 / 上移 / 下移 -->
          <template v-else-if="ctxMenu.kind === 'pin'">
            <button class="ctx-item" @click="disconnectPin()">
              <Link2Off />
              <span>断开该引脚</span>
              <span class="ctx-desc">子节点转为游离</span>
            </button>
            <button v-if="(ctxMenu.pin?.count ?? 0) > 1" class="ctx-item danger" @click="removeOutputPin()">
              <Trash2 />
              <span>删除输出 {{ ctxMenu.pin?.idx }}</span>
              <span class="ctx-desc">子节点转为游离</span>
            </button>
            <button v-if="(ctxMenu.pin?.idx ?? 0) > 0" class="ctx-item" @click="moveOutputPin(-1)">
              <Undo2 />
              <span>上移输出</span>
            </button>
            <button
              v-if="(ctxMenu.pin?.idx ?? 0) < (ctxMenu.pin?.count ?? 0) - 1"
              class="ctx-item"
              @click="moveOutputPin(1)"
            >
              <Redo2 />
              <span>下移输出</span>
            </button>
          </template>

          <!-- 空白 / 连线 / 引脚拖出：搜索 + 一级分类（二级具体条目，可折叠） -->
          <template v-else>
            <div class="ctx-search">
              <Search />
              <input v-model="ctxMenu.search" placeholder="搜索类型 / 管线 / 子树…" @click.stop />
            </div>
            <div class="ctx-list">
              <template v-for="g in menuGroups" :key="g.key">
                <div class="ctx-group" @click.stop="toggleMenuGroup(g.key)">
                  <component :is="g.icon" />
                  <span>{{ g.label }}</span>
                  <span class="ctx-count">{{ g.items.length }}</span>
                  <ChevronRight class="ctx-caret" :class="{ open: menuOpen[g.key] || searching }" />
                </div>
                <template v-if="menuOpen[g.key] || searching">
                  <button v-for="item in g.items" :key="menuKey(item)" class="ctx-item sub" @click="pickMenuItem(item)">
                    <component :is="item.icon" />
                    <span class="ctx-main">{{ item.label }}</span>
                    <span class="ctx-desc">{{ item.desc }}</span>
                  </button>
                </template>
              </template>
              <div v-if="menuGroups.every((g) => g.items.length === 0)" class="ctx-empty">无匹配项</div>
            </div>
            <template v-if="ctxMenu.kind === 'edge'">
              <div class="ctx-sep" />
              <button class="ctx-item danger" @click="disconnectEdge()">
                <Trash2 />
                <span>断开该连接</span>
              </button>
            </template>
          </template>
        </div>
      </transition>

      <!-- 右侧属性面板（单选时显示；多选时提供批量删除等操作） -->
      <transition name="panel">
        <aside v-if="selectedNode && selectedKeys.size === 1" class="props-panel">
          <div class="props-head">
            <div class="props-title">
              <component :is="nodeHeadIcon(selectedNode.type)" :size="16" />
              <span>{{ nodeTypeLabel(selectedNode) }}</span>
              <span v-if="isFloatSelected" class="float-badge">游离</span>
            </div>
            <button class="props-close" @click="selectedKey = ''; selectedKeys = new Set()"><X :size="16" /></button>
          </div>

          <div class="props-tabs">
            <button :class="{ on: propsTab === 'props' }" @click="switchPropsTab('props')">属性</button>
            <button :class="{ on: propsTab === 'audit' }" @click="switchPropsTab('audit')">审计</button>
          </div>

          <!-- 属性 Tab -->
          <div v-if="propsTab === 'props'" class="props-body">
            <div class="field">
              <label>名称</label>
              <t-input :model-value="selectedNode.name ?? ''" @change="(v: any) => updateSelected({ name: String(v) })" />
            </div>
            <div class="field">
              <label>类型</label>
              <t-select :model-value="selectedNode.type" @change="(v: any) => replaceNodeType(v)">
                <t-option v-for="t in NODE_TYPES" :key="t.type" :value="t.type" :label="t.label" />
              </t-select>
            </div>
            <div v-if="selectedNode.type === 'condition'" class="field">
              <label>条件</label>
              <t-input
                :model-value="selectedNode.condition ?? ''"
                placeholder="如 group_id == 'xxx'"
                @change="(v: any) => updateSelected({ condition: String(v) })"
              />
            </div>
            <div v-if="selectedNode.type === 'action'" class="field">
              <label>管线</label>
              <t-select
                :model-value="selectedNode.pipeline_id ?? ''"
                clearable
                @change="(v: any) => updateSelected({ pipeline_id: v ? String(v) : undefined })"
              >
                <t-option v-for="p in snapshot?.pipelines ?? []" :key="p.id" :value="p.id" :label="`管线 ${p.id}`" />
              </t-select>
            </div>
            <div v-if="selectedNode.type === 'subtree'" class="field">
              <label>子树</label>
              <t-select
                :model-value="selectedNode.subtree_id ?? ''"
                clearable
                @change="(v: any) => updateSelected({ subtree_id: v ? String(v) : undefined })"
              >
                <t-option v-for="s in snapshot?.subtrees ?? []" :key="s.id" :value="s.id" :label="`子树 ${s.id}`" />
              </t-select>
            </div>
            <div v-if="selectedNode.type === 'selector' || selectedNode.type === 'sequence'" class="field">
              <label>子节点（{{ selectedNode.children?.length ?? 0 }}）</label>
              <div class="child-add">
                <button v-for="t in NODE_TYPES" :key="t.type" class="chip" @click="addChild(t.type)">
                  <component :is="t.icon" :size="13" /> {{ t.label }}
                </button>
              </div>
            </div>
          </div>

          <!-- 审计 Tab -->
          <div v-else class="props-body">
            <template v-if="selectedNode.type !== 'action' || !selectedNode.pipeline_id">
              <div class="audit-empty">仅动作节点（引用管线）有审计数据</div>
            </template>
            <template v-else-if="panelAudit">
              <div class="audit-stats">
                <div class="stat"><b>{{ panelAudit.total }}</b><span>总数</span></div>
                <div class="stat ok"><b>{{ panelAudit.ok }}</b><span>成功</span></div>
                <div class="stat err"><b>{{ panelAudit.err }}</b><span>失败</span></div>
                <div class="stat"><b>{{ panelAudit.avgMs }}</b><span>均耗 ms</span></div>
              </div>
              <div class="audit-list">
                <div v-for="t in panelAudit.items" :key="t.id" class="audit-item">
                  <span class="dot" :class="t.status" />
                  <span class="audit-time">{{ fmtTime(t.created_at) }}</span>
                  <span class="audit-dur">{{ t.duration_ms }}ms</span>
                  <span class="audit-st">{{ t.status }}</span>
                </div>
                <div v-if="panelAudit.items.length === 0" class="audit-empty">近 24h 无执行记录</div>
              </div>
            </template>
            <div v-else class="audit-empty">加载中…</div>
          </div>

          <div class="props-foot">
            <t-button theme="danger" variant="outline" block @click="removeSelected()">
              <template #icon><Trash2 :size="15" /></template>
              删除节点
            </t-button>
          </div>
        </aside>
      </transition>
    </div>

    <!-- ═══════════ JSON DSL 模式 ═══════════ -->
    <div v-else class="json-wrap">
      <div class="json-head">
        <div class="json-title">
          <Code2 :size="15" />
          <span>{{ editCtx.kind === 'subtree' ? `子树 ${editCtx.id} · JSON DSL` : '行为树 · JSON DSL' }}</span>
          <button v-if="editCtx.kind === 'subtree'" class="crumb-back" title="返回上一级" @click="exitSubtree()">
            <ArrowLeft :size="14" />
          </button>
        </div>
        <div class="tool-seg">
          <button :class="{ on: isMode('blueprint') }" @click="setMode('blueprint')">蓝图</button>
          <button :class="{ on: isMode('json') }" @click="setMode('json')">JSON</button>
        </div>
      </div>
      <div class="json-editor">
        <Codemirror
          v-model="jsonText"
          :extensions="codemirrorExtensions"
          :style="{ height: '100%' }"
          placeholder="行为树 JSON DSL（应用变更请使用底部悬浮条）"
        />
      </div>
    </div>

    <!-- 底部悬浮保存条（仅存在未保存变更时） -->
    <transition name="savebar-fade">
      <div v-if="dirty" class="canvas-savebar">
        <SaveBar :visible="true" @save="onSaveRequested" @cancel="discardChanges" />
      </div>
    </transition>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
/* ══════════ 布局 ══════════ */
.bt-view {
  position: relative;
  width: 100%;
  height: 100%;
  overflow: hidden;
  background: var(--mgr-bg);
}

.bp-wrap {
  position: absolute;
  inset: 0;
}
.bp-wrap .vue-flow {
  width: 100%;
  height: 100%;
  background: radial-gradient(circle, var(--mgr-border) 1px, transparent 1.4px);
  background-size: 26px 26px;
}
.bp-wrap.no-grid .vue-flow {
  background: transparent;
}
.bp-wrap .vue-flow__node {
  background: transparent;
  border: none;
  box-shadow: none;
  padding: 0;
  width: auto;
}

/* ══════════ 连线 ══════════
   注意：边/引脚/连接线由 Vue Flow 内部渲染，scoped 样式必须用 :deep() 穿透，
   否则选择器被加上 data-v 属性而失效（此前 hover 加粗不生效、dark 下聚焦线变暗的根因）。 */
.bp-wrap :deep(.vue-flow__edge-path) {
  stroke: var(--mgr-text-muted);
  stroke-width: 1.6;
  transition: stroke 0.15s ease, stroke-width 0.15s ease, filter 0.2s ease;
}
/* hover：明显加粗 + 固定琥珀色辉光（light/dark 均清晰，避免 dark 下变暗） */
.bp-wrap :deep(.vue-flow__edge:hover .vue-flow__edge-path) {
  stroke: #ffc84d;
  stroke-width: 3;
  filter: drop-shadow(0 0 5px rgba(255, 200, 77, 0.5));
}
/* selected：高亮琥珀色 + 加粗 + 辉光（清晰区分） */
.bp-wrap :deep(.vue-flow__edge.selected .vue-flow__edge-path) {
  stroke: #ffc84d;
  stroke-width: 3.2;
  filter: drop-shadow(0 0 6px rgba(255, 200, 77, 0.55));
}
.bp-wrap :deep(.vue-flow__connection-path) {
  stroke: var(--mgr-primary);
  stroke-width: 2;
}
/* 执行流动画：流动线加粗 + 流光（优先级最高，覆盖 hover/selected） */
.bp-wrap :deep(.vue-flow__edge.flowing .vue-flow__edge-path) {
  stroke: var(--mgr-primary);
  stroke-width: 3.4;
  stroke-dasharray: 10 7;
  animation: dash-flow 0.55s linear infinite;
  filter: drop-shadow(0 0 6px var(--mgr-primary-soft));
}
@keyframes dash-flow {
  to {
    stroke-dashoffset: -17;
  }
}
/* 拖线预览边：虚线流光，提示"将从源节点连出新节点" */
.bp-wrap :deep(.vue-flow__edge.preview-connect .vue-flow__edge-path) {
  stroke: var(--mgr-primary);
  stroke-width: 2.4;
  stroke-dasharray: 7 5;
  animation: dash-flow 0.6s linear infinite;
  filter: drop-shadow(0 0 5px var(--mgr-primary-soft));
  opacity: 0.85;
}

/* ══════════ 引脚 ══════════ */
.bp-wrap :deep(.vue-flow__handle) {
  width: 13px;
  height: 13px;
  min-width: 13px;
  min-height: 13px;
  border-radius: 50%;
  background: var(--mgr-bg-card);
  border: 2.5px solid var(--mgr-primary);
  transition: background 0.15s ease, transform 0.15s ease, box-shadow 0.15s ease;
}
.bp-wrap :deep(.vue-flow__handle:hover),
.bp-wrap :deep(.vue-flow__handle.connecting) {
  background: var(--mgr-primary);
  box-shadow: 0 0 8px var(--mgr-primary-soft);
}

/* 拖线预览锚点：释放位置的小圆点占位 */
.ue-preview-anchor {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  border: 2px dashed var(--mgr-primary);
  background: var(--mgr-primary-soft);
  box-shadow: 0 0 8px var(--mgr-primary-soft);
  pointer-events: none;
}

/* ══════════ 节点卡片（虚幻蓝图风格） ══════════ */
.ue-node {
  position: relative;
  width: 232px;
  background: var(--mgr-bg-card);
  border: 1px solid var(--mgr-border-strong);
  border-radius: 10px;
  box-shadow: var(--mgr-shadow-sm);
  overflow: visible; /* 引脚贴边显示，不能被裁剪 */
  cursor: grab;
  transition: border-color 0.15s ease, box-shadow 0.15s ease, transform 0.15s ease;
}
.vue-flow__node:hover .ue-node {
  border-color: var(--mgr-primary);
  box-shadow: var(--mgr-shadow-md);
  transform: translateY(-1px);
}
.vue-flow__node.selected .ue-node {
  border-color: var(--mgr-primary);
  box-shadow: 0 0 0 2px var(--mgr-primary-soft), var(--mgr-shadow-md);
}
.ue-head {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 10px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.4px;
  color: #fff;
  border-radius: 9px 9px 0 0;
  overflow: hidden; /* 仅头部圆角裁剪，不影响引脚 */
}
.ue-head.sel {
  background: linear-gradient(135deg, #3b6fd4, #2f5cb8);
}
.ue-head.seq {
  background: linear-gradient(135deg, #3f9e6f, #2f7d56);
}
.ue-head.cond {
  background: linear-gradient(135deg, #c98f3f, #a06e2c);
}
.ue-head.act {
  background: linear-gradient(135deg, #8b5cc9, #6f44a8);
}
.ue-head.sub {
  background: linear-gradient(135deg, #2e9aa8, #227c8a);
}
.ue-label {
  padding: 9px 12px 10px;
  font-size: 13px;
  line-height: 1.45;
  color: var(--mgr-text);
  word-break: break-all;
}
.float-badge {
  margin-left: auto;
  font-size: 10px;
  line-height: 1;
  padding: 3px 6px;
  border-radius: 999px;
  background: var(--mgr-warning);
  color: #fff;
  font-weight: 700;
}

/* ══════════ 动态输出引脚（选择 / 顺序节点） ══════════ */
.ue-dyn .ue-label {
  padding: 6px 12px 7px;
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ue-outs {
  border-top: 1px solid var(--mgr-border);
}
.ue-out-row {
  position: relative; /* 作为输出引脚的定位祖先，使每个 handle 相对各自行垂直居中 */
  display: flex;
  align-items: center;
  justify-content: flex-end; /* 条目整体向右对齐，序号贴近右侧引脚 */
  gap: 6px;
  height: 24px;
  padding: 0 8px;
  font-size: 12px;
  color: var(--mgr-text-secondary);
  cursor: default;
}
.ue-out-row:hover {
  background: var(--mgr-bg-hover);
}
.ue-out-idx {
  flex: none;
  width: 14px;
  height: 14px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 4px;
  background: var(--mgr-primary-soft);
  color: var(--mgr-primary);
  font-size: 10px;
  font-weight: 700;
}
.ue-out-name {
  flex: 1; /* 名称占据剩余空间并靠右，序号紧跟右侧引脚 */
  text-align: right;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.ue-add-out {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  width: 100%;
  height: 22px;
  padding: 0 8px;
  border: none;
  border-top: 1px dashed var(--mgr-border);
  background: transparent;
  color: var(--mgr-primary);
  font-size: 11px;
  cursor: pointer;
}
.ue-add-out:hover {
  background: var(--mgr-primary-soft);
}
/* 输出引脚 handle：相对行（.ue-out-row 为定位祖先），贴右缘 */
.bp-wrap :deep(.vue-flow__handle.ue-out-handle) {
  right: -7px;
  width: 12px;
  height: 12px;
  min-width: 12px;
  min-height: 12px;
}

/* 右键框选矩形（fixed 相对视口，与 client 坐标一致） */
.box-rect {
  position: fixed;
  z-index: 90;
  border: 1px dashed var(--mgr-primary);
  background: var(--mgr-primary-soft);
  pointer-events: none;
}

/* 框选多选高亮 */
.vue-flow__node.box-sel .ue-node {
  border-color: var(--mgr-primary);
  box-shadow: 0 0 0 2px var(--mgr-primary-soft), var(--mgr-shadow-md);
}

/* 工具栏禁用态 */
.tool-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}

/* Start 节点 */
.ue-start {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 18px;
  background: var(--mgr-bg-card);
  border: 1.5px solid var(--mgr-primary);
  border-radius: 999px;
  color: var(--mgr-primary);
  font-weight: 700;
  font-size: 13px;
  letter-spacing: 0.6px;
  box-shadow: var(--mgr-shadow-sm);
  cursor: grab;
}

/* 执行中节点：脉冲聚焦动画 */
.vue-flow__node.executing .ue-node {
  border-color: var(--mgr-primary);
  box-shadow: 0 0 0 4px var(--mgr-primary-soft), 0 0 30px var(--mgr-primary-soft);
  animation: node-pulse 0.85s ease-in-out infinite;
}
@keyframes node-pulse {
  0%,
  100% {
    transform: scale(1);
  }
  50% {
    transform: scale(1.06);
  }
}

/* ══════════ 面包屑 ══════════ */
.bp-breadcrumb {
  position: absolute;
  top: 14px;
  left: 16px;
  z-index: 30;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 12px;
  background: var(--mgr-bg-card);
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  box-shadow: var(--mgr-shadow-sm);
  font-size: 13px;
}
.crumb {
  border: none;
  background: none;
  color: var(--mgr-text-secondary);
  font-size: 13px;
  cursor: pointer;
  padding: 0;
}
.crumb.on {
  color: var(--mgr-primary);
  font-weight: 600;
}
.crumb-sep {
  color: var(--mgr-text-muted);
}
.crumb-back {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  margin-left: 4px;
  border: 1px solid var(--mgr-border-strong);
  border-radius: 50%;
  background: none;
  color: var(--mgr-text-secondary);
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s, background 0.15s;
}
.crumb-back:hover {
  color: var(--mgr-primary);
  border-color: var(--mgr-primary);
  background: var(--mgr-primary-soft);
}

/* ══════════ 悬浮工具栏 ══════════ */
.bp-toolbar {
  position: absolute;
  top: 14px;
  right: 16px;
  z-index: 30;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px;
  background: var(--mgr-bg-card);
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  box-shadow: var(--mgr-shadow-sm);
}
.tool-seg {
  display: flex;
  padding: 2px;
  background: var(--mgr-bg-secondary);
  border-radius: 8px;
}
.tool-seg button {
  border: none;
  background: none;
  padding: 5px 12px;
  font-size: 12px;
  color: var(--mgr-text-secondary);
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}
.tool-seg button.on {
  background: var(--mgr-bg-card);
  color: var(--mgr-primary);
  font-weight: 600;
  box-shadow: var(--mgr-shadow-sm);
}
.tool-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: none;
  background: none;
  color: var(--mgr-text-secondary);
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s, color 0.15s;
}
.tool-btn:hover,
.tool-btn.on {
  background: var(--mgr-bg-hover);
  color: var(--mgr-primary);
}
.tool-sep {
  width: 1px;
  height: 20px;
  background: var(--mgr-border);
}
.live-tag {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 0 10px;
  font-size: 12px;
  font-weight: 600;
  color: var(--mgr-primary);
}
.live-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--mgr-primary);
  animation: live-blink 1s ease-in-out infinite;
}
@keyframes live-blink {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.25;
  }
}
.live-enter-active,
.live-leave-active {
  transition: opacity 0.2s ease;
}
.live-enter-from,
.live-leave-to {
  opacity: 0;
}

/* ══════════ 悬浮审计面板（毛玻璃） ══════════ */
.hover-audit {
  position: fixed;
  z-index: 50;
  width: 300px;
  padding: 12px 14px;
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  box-shadow: var(--mgr-shadow-lg);
  background: var(--mgr-bg-card);
  color: var(--mgr-text);
}
.is-dark .hover-audit {
  background: rgba(34, 34, 38, 0.82);
  backdrop-filter: blur(14px) saturate(1.4);
  -webkit-backdrop-filter: blur(14px) saturate(1.4);
}
.audit-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}
.audit-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--mgr-text);
}
.audit-close {
  display: inline-flex;
  color: var(--mgr-text-muted);
  cursor: pointer;
  transition: color 0.15s;
}
.audit-close:hover {
  color: var(--mgr-danger);
}
.audit-stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 6px;
  margin-bottom: 10px;
}
.stat {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  padding: 6px 2px;
  background: var(--mgr-bg-secondary);
  border-radius: 8px;
}
.stat b {
  font-size: 16px;
  color: var(--mgr-text);
}
.stat span {
  font-size: 11px;
  color: var(--mgr-text-muted);
}
.stat.ok b {
  color: var(--mgr-success);
}
.stat.err b {
  color: var(--mgr-danger);
}
.audit-list {
  display: flex;
  flex-direction: column;
  gap: 4px;
  max-height: 180px;
  overflow-y: auto;
}
.audit-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--mgr-text-secondary);
  padding: 4px 6px;
  border-radius: 6px;
}
.audit-item:hover {
  background: var(--mgr-bg-hover);
}
.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--mgr-text-muted);
  flex: none;
}
.dot.ok {
  background: var(--mgr-success);
}
.dot.error {
  background: var(--mgr-danger);
}
.audit-time {
  color: var(--mgr-text-muted);
}
.audit-dur {
  margin-left: auto;
}
.audit-st {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--mgr-bg-active);
  color: var(--mgr-text-secondary);
}
.audit-empty {
  padding: 12px 0;
  text-align: center;
  font-size: 12px;
  color: var(--mgr-text-muted);
}
.audit-enter-active,
.audit-leave-active {
  transition: opacity 0.18s ease, transform 0.18s ease;
}
.audit-enter-from,
.audit-leave-to {
  opacity: 0;
  transform: translateY(-4px) scale(0.98);
}

/* ══════════ 右键菜单（毛玻璃） ══════════ */
.ctx-menu {
  position: fixed;
  z-index: 60;
  width: 280px;
  max-height: 70vh;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  box-shadow: var(--mgr-shadow-lg);
  background: var(--mgr-bg-card);
  overflow: hidden;
}
.is-dark .ctx-menu {
  background: rgba(34, 34, 38, 0.88);
  backdrop-filter: blur(14px) saturate(1.4);
  -webkit-backdrop-filter: blur(14px) saturate(1.4);
}
.ctx-head {
  padding: 10px 12px;
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 0.6px;
  color: var(--mgr-primary);
  border-bottom: 1px solid var(--mgr-border);
}
.ctx-subtitle {
  padding: 8px 14px 2px;
  font-size: 11px;
  font-weight: 600;
  color: var(--mgr-text-muted);
  letter-spacing: 0.4px;
}
.ctx-search {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 8px 10px;
  padding: 6px 10px;
  border: 1px solid var(--mgr-border-strong);
  border-radius: 8px;
  color: var(--mgr-text-muted);
  background: var(--mgr-bg-secondary);
}
.ctx-search input {
  flex: 1;
  border: none;
  outline: none;
  background: none;
  font-size: 13px;
  color: var(--mgr-text);
}
.ctx-list {
  flex: 1;
  overflow-y: auto;
  padding: 2px 6px 6px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.ctx-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 10px;
  border: none;
  background: none;
  border-radius: 8px;
  font-size: 13px;
  color: var(--mgr-text);
  cursor: pointer;
  text-align: left;
  transition: background 0.12s ease;
}
/* 二级条目：一级分类下缩进 */
.ctx-item.sub {
  padding-left: 22px;
}
/* 菜单内图标统一尺寸（消除大小不一） */
.ctx-item > svg,
.ctx-group > svg,
.ctx-search > svg {
  width: 15px;
  height: 15px;
  flex: none;
}
.ctx-item:hover {
  background: var(--mgr-bg-hover);
}
.ctx-item .ctx-main {
  flex: none;
}
.ctx-item .ctx-desc {
  margin-left: auto;
  font-size: 11px;
  color: var(--mgr-text-muted);
  white-space: nowrap;
}
/* 断连二级菜单条目：目标节点小圆点 */
.ctx-item .ctx-dot {
  width: 7px;
  height: 7px;
  flex: none;
  border-radius: 50%;
  background: var(--mgr-primary);
  opacity: 0.7;
}
/* 一级分类行（可折叠） */
.ctx-group {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 10px;
  margin-top: 2px;
  border-radius: 8px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--mgr-text-secondary);
  cursor: pointer;
  user-select: none;
  transition: background 0.12s ease;
}
.ctx-group:hover {
  background: var(--mgr-bg-hover);
  color: var(--mgr-text);
}
.ctx-count {
  margin-left: auto;
  font-size: 11px;
  line-height: 1;
  padding: 3px 7px;
  border-radius: 999px;
  background: var(--mgr-bg-secondary);
  color: var(--mgr-text-muted);
}
.ctx-caret {
  color: var(--mgr-text-muted);
  transition: transform 0.15s ease;
}
.ctx-caret.open {
  transform: rotate(90deg);
}
.ctx-item.danger {
  color: var(--mgr-danger);
}
.ctx-item.danger:hover {
  background: color-mix(in srgb, var(--mgr-danger) 12%, transparent);
}
.ctx-item.back {
  color: var(--mgr-text-secondary);
  font-size: 12px;
}
.ctx-sep {
  height: 1px;
  margin: 4px 10px;
  background: var(--mgr-border);
}
.ctx-empty {
  padding: 16px 0;
  text-align: center;
  font-size: 12px;
  color: var(--mgr-text-muted);
}
.menu-enter-active,
.menu-leave-active {
  transition: opacity 0.14s ease, transform 0.14s ease;
}
.menu-enter-from,
.menu-leave-to {
  opacity: 0;
  transform: scale(0.96) translateY(-4px);
}

/* ══════════ 右侧属性面板 ══════════ */
.props-panel {
  position: absolute;
  top: 56px;
  right: 16px;
  bottom: 80px;
  z-index: 35;
  width: 296px;
  display: flex;
  flex-direction: column;
  background: var(--mgr-bg-card);
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  box-shadow: var(--mgr-shadow-lg);
  overflow: hidden;
}
.props-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px;
  border-bottom: 1px solid var(--mgr-border);
}
.props-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--mgr-text);
}
.props-close {
  display: inline-flex;
  border: none;
  background: none;
  color: var(--mgr-text-muted);
  cursor: pointer;
  transition: color 0.15s;
}
.props-close:hover {
  color: var(--mgr-danger);
}
/* 属性 / 审计 Tab */
.props-tabs {
  display: flex;
  gap: 4px;
  padding: 8px 12px 0;
  border-bottom: 1px solid var(--mgr-border);
  flex: none;
}
.props-tabs button {
  flex: 1;
  padding: 7px 0 9px;
  border: none;
  background: none;
  font-size: 13px;
  color: var(--mgr-text-muted);
  cursor: pointer;
  border-bottom: 2px solid transparent;
  transition: color 0.15s ease, border-color 0.15s ease;
}
.props-tabs button:hover {
  color: var(--mgr-text);
}
.props-tabs button.on {
  color: var(--mgr-primary);
  border-bottom-color: var(--mgr-primary);
  font-weight: 600;
}
.props-body {
  flex: 1;
  overflow-y: auto;
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.field > label {
  font-size: 12px;
  font-weight: 600;
  color: var(--mgr-text-secondary);
}
.child-add {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 5px 10px;
  border: 1px solid var(--mgr-border-strong);
  border-radius: 999px;
  background: none;
  font-size: 12px;
  color: var(--mgr-text-secondary);
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s, background 0.15s;
}
.chip:hover {
  color: var(--mgr-primary);
  border-color: var(--mgr-primary);
  background: var(--mgr-primary-soft);
}
.props-foot {
  padding: 12px 14px;
  border-top: 1px solid var(--mgr-border);
}
.panel-enter-active,
.panel-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}
.panel-enter-from,
.panel-leave-to {
  opacity: 0;
  transform: translateX(16px);
}

/* ══════════ JSON DSL 模式 ══════════ */
.json-wrap {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  padding: 16px;
  gap: 12px;
}
.json-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex: none;
}
.json-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
  color: var(--mgr-text);
}
.json-editor {
  flex: 1;
  min-height: 0;
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  overflow: hidden;
  background: var(--mgr-bg-card);
}
.json-editor :deep(.cm-editor) {
  height: 100%;
  font-size: 13px;
}
/* JSON DSL 编辑器统一使用 Cascadia Code 等宽字体（含行号栏） */
.json-editor :deep(.cm-scroller),
.json-editor :deep(.cm-content),
.json-editor :deep(.cm-gutters) {
  font-family: 'Cascadia Code', 'JetBrains Mono', Consolas, 'Courier New', monospace;
}

/* ══════════ 底部悬浮保存条 ══════════ */
.canvas-savebar {
  position: absolute;
  left: 50%;
  bottom: 22px;
  transform: translateX(-50%);
  z-index: 70;
  min-width: 560px;
  max-width: 720px;
}
.savebar-fade-enter-active,
.savebar-fade-leave-active {
  transition: opacity 0.25s ease, transform 0.25s ease;
}
.savebar-fade-enter-from,
.savebar-fade-leave-to {
  opacity: 0;
  transform: translateX(-50%) translateY(16px);
}
</style>
