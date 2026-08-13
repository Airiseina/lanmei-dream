<script setup lang="ts">
// 表情包库管理：列表 + 语义标签编辑 + 删除（需 super + step-up）
import { onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol } from 'tdesign-vue-next'
import { Tag, Trash2 } from 'lucide-vue-next'
import { contentApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { StickerView } from '@/types/api'

const columns: PrimaryTableCol[] = [
  { colKey: 'id', title: 'ID', width: 70, align: 'center' },
  { colKey: 'object_key', title: '对象键' },
  { colKey: 'tags', title: '语义标签', width: 260 },
  { colKey: 'source', title: '来源', width: 120 },
  { colKey: 'created_at', title: '收藏时间', width: 180 },
  { colKey: 'ops', title: '操作', width: 160, fixed: 'right' },
]

const auth = useAuthStore()

const items = ref<StickerView[]>([])
const total = ref(0)
const loading = ref(false)
const keyword = ref('')
const page = ref(1)
const pageSize = 20

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

// 标签编辑弹窗
const editVisible = ref(false)
const editing = ref<StickerView | null>(null)
const tagsText = ref('')

async function load() {
  loading.value = true
  try {
    const res = await contentApi.stickers(keyword.value, page.value, pageSize)
    items.value = res.items
    total.value = res.total
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  } finally {
    loading.value = false
  }
}

function pageChange(p: { current: number }) {
  page.value = p.current
  void load()
}

function search() {
  page.value = 1
  void load()
}

function openEdit(s: StickerView) {
  editing.value = s
  tagsText.value = (s.tags ?? []).join('，')
  editVisible.value = true
}

async function saveTags() {
  if (!editing.value) return
  const tags = tagsText.value
    .split(/[，,、\n]/)
    .map((t) => t.trim())
    .filter(Boolean)
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await contentApi.updateSticker(editing.value!.id, tags, token)
    MessagePlugin.success('标签已更新')
    editVisible.value = false
    await load()
  }
}

function remove(s: StickerView) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await contentApi.deleteSticker(s.id, token)
    MessagePlugin.success('表情已删除')
    await load()
  }
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

function formatTime(s: string): string {
  return new Date(s).toLocaleString()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">表情包库</h2>
      <div class="header-search">
        <t-input
          v-model="keyword"
          placeholder="搜索标签 / 来源…"
          clearable
          :style="{ width: 220 }"
          @enter="search"
          @clear="search"
        />
        <t-button theme="primary" :loading="loading" @click="search">查询</t-button>
      </div>
    </div>

    <t-card>
      <t-table
        :data="items"
        :loading="loading"
        row-key="id"
        :pagination="{ current: page, pageSize, total, onChange: pageChange }"
        :columns="columns"
      >
        <template #id="{ row }">{{ row.id }}</template>
        <template #object_key="{ row }">
          <span class="key">{{ row.object_key }}</span>
        </template>
        <template #tags="{ row }">
          <t-space v-if="row.tags.length" size="4" wrap>
            <t-tag v-for="t in row.tags" :key="t" theme="primary" variant="light" size="small">{{ t }}</t-tag>
          </t-space>
          <span v-else class="muted">无</span>
        </template>
        <template #source="{ row }">{{ row.source || '—' }}</template>
        <template #created_at="{ row }">{{ formatTime(row.created_at) }}</template>
        <template #ops="{ row }">
          <t-space v-if="auth.isSuper" size="4">
            <t-button size="small" variant="base" @click="openEdit(row)">
              <template #icon><Tag :size="14" /></template>标签
            </t-button>
            <t-button size="small" variant="base" theme="danger" @click="remove(row)">
              <template #icon><Trash2 :size="14" /></template>删除
            </t-button>
          </t-space>
        </template>
      </t-table>
    </t-card>

    <!-- 标签编辑 -->
    <t-dialog
      v-model:visible="editVisible"
      header="编辑语义标签"
      :confirm-btn="{ content: '保存', theme: 'primary' }"
      cancel-btn="取消"
      @confirm="saveTags"
    >
      <p class="muted">用逗号或顿号分隔多个标签（如：大怨种，无语，翻白眼）</p>
      <t-textarea v-model="tagsText" :autosize="{ minRows: 3, maxRows: 6 }" placeholder="输入标签…" />
    </t-dialog>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.key {
  max-width: 360px;
  display: inline-block;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
}
.muted {
  color: var(--mgr-text-secondary);
}
</style>
