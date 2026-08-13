<script setup lang="ts">
// 管线管理：查看/编辑管线 Pass 序列（静态管线只读）。
// 追加 Pass 以图形化按钮跟在链尾；保存仅在有变更时以底部悬浮条出现，需 super + step-up。
import { onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { ArrowDown, ArrowUp, Plus, X } from 'lucide-vue-next'
import { conduitApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import SaveBar from '@/components/SaveBar.vue'
import type { ConduitSnapshot, PipelineView } from '@/types/api'

const auth = useAuthStore()

const snapshot = ref<ConduitSnapshot | null>(null)
const loading = ref(false)
const dirty = ref(false)
const comment = ref('')
// 当前展开追加菜单的管线 id
const addingFor = ref<string>('')

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

async function load() {
  loading.value = true
  try {
    snapshot.value = await conduitApi.snapshot()
    dirty.value = false
    addingFor.value = ''
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  } finally {
    loading.value = false
  }
}

function movePass(pipeline: PipelineView, index: number, dir: -1 | 1) {
  const target = index + dir
  if (target < 0 || target >= pipeline.pass_ids.length) return
  const arr = [...pipeline.pass_ids]
  ;[arr[index], arr[target]] = [arr[target], arr[index]]
  pipeline.pass_ids = arr
  dirty.value = true
}

function removePass(pipeline: PipelineView, index: number) {
  pipeline.pass_ids = pipeline.pass_ids.filter((_, i) => i !== index)
  dirty.value = true
}

function pickPass(pipeline: PipelineView, passId: string) {
  if (pipeline.pass_ids.includes(passId)) {
    MessagePlugin.warning('该 Pass 已在管线中')
    return
  }
  pipeline.pass_ids = [...pipeline.pass_ids, passId]
  dirty.value = true
  addingFor.value = ''
}

// 保存：悬浮条触发 → step-up 二次验证后应用
function onSaveRequested(c: string) {
  comment.value = c
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    const pipelines = (snapshot.value?.pipelines ?? [])
      .filter((p) => !p.readonly)
      .map((p) => ({ id: p.id, pass_ids: p.pass_ids }))
    await conduitApi.applyPipelines(pipelines, comment.value, token)
    MessagePlugin.success('管线已更新')
    await load()
  }
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">管线</h2>
      <t-button variant="outline" :loading="loading" @click="load">刷新</t-button>
    </div>

    <t-alert
      theme="info"
      message="管线由 Pass 顺序执行组成。静态管线（readonly）由代码注册不可编辑；动态管线可调整 Pass 顺序、追加/移除 Pass。"
      class="mb-16"
    />

    <t-card :loading="loading">
      <template v-if="snapshot">
        <div v-for="p in snapshot.pipelines" :key="p.id" class="pipeline-card">
          <div class="pipeline-head">
            <div class="pipeline-title">
              <span class="pipeline-id">{{ p.id }}</span>
              <t-tag v-if="p.readonly" variant="light" theme="default">静态只读</t-tag>
              <t-tag v-else variant="light" theme="primary">可编辑</t-tag>
            </div>
            <div class="pipeline-steps">
              <template v-for="(passId, idx) in p.pass_ids" :key="passId">
                <div class="step">
                  <span class="step-index">{{ idx + 1 }}</span>
                  <t-tag variant="outline" theme="primary">{{ passId }}</t-tag>
                  <template v-if="!p.readonly">
                    <t-button variant="text" shape="square" size="small" :disabled="idx === 0" @click="movePass(p, idx, -1)">
                      <ArrowUp :size="14" />
                    </t-button>
                    <t-button variant="text" shape="square" size="small" :disabled="idx === p.pass_ids.length - 1" @click="movePass(p, idx, 1)">
                      <ArrowDown :size="14" />
                    </t-button>
                    <t-button variant="text" shape="square" size="small" theme="danger" @click="removePass(p, idx)">
                      <X :size="14" />
                    </t-button>
                  </template>
                </div>
                <span v-if="idx < p.pass_ids.length - 1" class="arrow">→</span>
              </template>
              <span v-if="!p.pass_ids.length" class="empty">（空管线）</span>

              <!-- 追加 Pass：图形化 + 按钮跟在链尾，点击弹出可选 Pass 列表 -->
              <t-popup
                v-if="!p.readonly"
                trigger="click"
                placement="bottom-start"
                :visible="addingFor === p.id"
                @visible-change="(v: boolean) => (addingFor = v ? p.id : '')"
              >
                <t-button shape="round" variant="outline" size="small" class="add-pass-btn" @click="addingFor = p.id">
                  <template #icon><Plus :size="15" /></template>
                  追加
                </t-button>
                <template #content>
                  <div class="add-pass-menu">
                    <div v-for="ps in snapshot?.passes ?? []" :key="ps.id" class="add-pass-item" @click="pickPass(p, ps.id)">
                      <span class="pass-id">{{ ps.id }}</span>
                      <span class="pass-type">{{ ps.type_name }}</span>
                      <Plus v-if="!p.pass_ids.includes(ps.id)" :size="14" class="pass-add-icon" />
                      <span v-else class="pass-in">已在链中</span>
                    </div>
                  </div>
                </template>
              </t-popup>
            </div>
          </div>
        </div>
      </template>
    </t-card>

    <!-- 底部悬浮保存条（仅变更时出现） -->
    <div class="savebar-wrap">
      <SaveBar v-if="auth.isSuper" :visible="dirty" @save="onSaveRequested" @cancel="load" />
      <p v-else-if="dirty" class="perm-tip">仅超级管理员可修改管线</p>
    </div>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.mb-16 {
  margin-bottom: 16px;
}
.pipeline-card {
  border: 1px solid var(--mgr-border);
  border-radius: 8px;
  padding: 14px;
  margin-bottom: 12px;
}
.pipeline-head {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.pipeline-title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pipeline-id {
  font-weight: 600;
  font-family: 'SFMono-Regular', Consolas, monospace;
}
.pipeline-steps {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.step {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}
.step-index {
  font-size: 12px;
  color: var(--mgr-text-secondary);
  margin-right: 4px;
}
.arrow {
  color: var(--mgr-text-secondary);
}
.empty {
  color: var(--mgr-text-secondary);
}

/* 追加按钮（链尾图形化） */
.add-pass-btn {
  margin-left: 8px;
  border-style: dashed;
  color: var(--mgr-text-secondary);
}

.add-pass-menu {
  max-height: 280px;
  overflow-y: auto;
  background: var(--mgr-bg-card);
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  box-shadow: var(--mgr-shadow-lg);
  padding: 4px;
  min-width: 240px;
}

.add-pass-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-radius: 6px;
  cursor: pointer;
}

.add-pass-item:hover {
  background: var(--mgr-bg-hover);
}

.pass-id {
  font-weight: 600;
  font-size: 13px;
  font-family: 'SFMono-Regular', Consolas, monospace;
}

.pass-type {
  flex: 1;
  font-size: 12px;
  color: var(--mgr-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pass-add-icon {
  color: var(--mgr-primary);
}

.pass-in {
  font-size: 11px;
  color: var(--mgr-text-muted);
}

/* 底部悬浮保存条 */
.savebar-wrap {
  position: sticky;
  bottom: 12px;
  margin-top: 16px;
  display: flex;
  justify-content: center;
  z-index: 20;
}

.savebar-wrap > * {
  width: min(720px, 100%);
}

.perm-tip {
  text-align: center;
  color: var(--mgr-text-secondary);
  font-size: 13px;
  margin: 0;
  background: var(--mgr-bg-card);
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  padding: 10px;
}
</style>
