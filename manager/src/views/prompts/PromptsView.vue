<script setup lang="ts">
// Prompt 模板管理：fragment 列表 + 查看/编辑（builtin 只读），保存后热重载
import { computed, onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol } from 'tdesign-vue-next'
import { Codemirror } from 'vue-codemirror'
import { oneDark } from '@codemirror/theme-one-dark'
import { Eye, Lock } from 'lucide-vue-next'
import { contentApi } from '@/api'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { PromptFragmentView } from '@/types/api'

const columns: PrimaryTableCol[] = [
  { colKey: 'id', title: 'ID', width: 180 },
  { colKey: 'file', title: '文件' },
  { colKey: 'builtin', title: '类型', width: 100 },
  { colKey: 'ops', title: '操作', width: 120, fixed: 'right' },
]

const app = useAppStore()
const auth = useAuthStore()

// Prompt 编辑器：Light 默认亮色；Dark 用 oneDark 高质量暗色方案
const codemirrorExtensions = computed(() => (app.theme === 'dark' ? [oneDark] : []))

const items = ref<PromptFragmentView[]>([])
const loading = ref(false)
const detailVisible = ref(false)
const current = ref<PromptFragmentView | null>(null)
const content = ref('')

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

async function load() {
  loading.value = true
  try {
    const res = await contentApi.promptFragments()
    items.value = res.items
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  } finally {
    loading.value = false
  }
}

async function openDetail(f: PromptFragmentView) {
  try {
    const detail = await contentApi.promptFragment(f.id)
    current.value = detail
    content.value = detail.content
    detailVisible.value = true
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  }
}

async function save() {
  if (!current.value) return
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await contentApi.updatePromptFragment(current.value!.id, content.value, token)
    MessagePlugin.success('已保存并热重载')
    detailVisible.value = false
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
      <h2 class="page-title">Prompt 模板</h2>
      <t-button theme="primary" variant="outline" :loading="loading" @click="load">刷新</t-button>
    </div>

    <t-alert
      theme="info"
      message="编辑非 builtin 片段后立即热重载生效；builtin 片段为工程内置只读内容。"
      class="mb-16"
    />

    <t-table :data="items" :loading="loading" row-key="id" :columns="columns">
      <template #id="{ row }">{{ row.id }}</template>
      <template #file="{ row }">{{ row.file }}</template>
      <template #builtin="{ row }">
        <t-tag :theme="row.builtin ? 'warning' : 'success'" variant="light">
          {{ row.builtin ? 'builtin' : '自定义' }}
        </t-tag>
      </template>
      <template #ops="{ row }">
        <t-button size="small" variant="base" @click="openDetail(row)">
          <template #icon><Eye :size="14" /></template>查看
        </t-button>
      </template>
    </t-table>

    <!-- 查看/编辑 -->
    <t-dialog
      v-model:visible="detailVisible"
      :header="current ? `${current.id}（${current.file}）` : ''"
      :width="720"
      :footer="false"
    >
      <div v-if="current" class="detail-wrap">
        <div v-if="current.builtin" class="readonly-tip">
          <Lock :size="14" />
          builtin 片段只读，不可修改
        </div>
        <Codemirror
          v-model="content"
          :readonly="current.builtin"
          :extensions="codemirrorExtensions"
          :style="{ height: '56vh', border: '1px solid var(--td-component-border)' }"
          placeholder="Prompt 内容…"
        />
        <t-space class="mt-16">
          <t-button v-if="auth.isSuper && !current.builtin" theme="primary" @click="save">保存</t-button>
          <t-button variant="outline" @click="detailVisible = false">关闭</t-button>
        </t-space>
      </div>
    </t-dialog>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.readonly-tip {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--mgr-text-secondary);
  font-size: 13px;
  margin-bottom: 8px;
}
/* Prompt 编辑器统一使用 Cascadia Code 等宽字体（含行号栏） */
.detail-wrap :deep(.cm-scroller),
.detail-wrap :deep(.cm-content),
.detail-wrap :deep(.cm-gutters) {
  font-family: 'Cascadia Code', 'JetBrains Mono', Consolas, 'Courier New', monospace;
}
</style>
