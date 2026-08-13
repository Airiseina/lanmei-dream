<script setup lang="ts">
// 知识库管理：知识库列表（元信息 + 分块数）+ 分块浏览/搜索/删除 + 内容重同步
import { onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol } from 'tdesign-vue-next'
import { RefreshCw, Trash2 } from 'lucide-vue-next'
import { contentApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { KnowledgeBaseView, KnowledgeChunk } from '@/types/api'

const columns: PrimaryTableCol[] = [
  { colKey: 'id', title: 'ID', width: 80, align: 'center' },
  { colKey: 'title', title: '标题', width: 220 },
  { colKey: 'content', title: '内容' },
  { colKey: 'source_id', title: '来源', width: 200 },
  { colKey: 'created_at', title: '更新时间', width: 160 },
  { colKey: 'ops', title: '操作', width: 90, fixed: 'right' },
]

const auth = useAuthStore()

const bases = ref<KnowledgeBaseView[]>([])
const chunks = ref<KnowledgeChunk[]>([])
const chunksTotal = ref(0)
const loading = ref(false)
const syncing = ref(false)
const currentBase = ref('')
const keyword = ref('')
const page = ref(1)
const pageSize = 20

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

async function loadBases() {
  try {
    const res = await contentApi.knowledgeBases()
    bases.value = res.items
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '知识库加载失败')
  }
}

async function loadChunks() {
  loading.value = true
  try {
    const res = await contentApi.knowledgeChunks({
      base: currentBase.value || undefined,
      keyword: keyword.value || undefined,
      page: page.value,
      pageSize,
    })
    chunks.value = res.items
    chunksTotal.value = res.total
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '分块加载失败')
  } finally {
    loading.value = false
  }
}

function pageChange(p: { current: number }) {
  page.value = p.current
  void loadChunks()
}

function search() {
  page.value = 1
  void loadChunks()
}

function selectBase(id: string) {
  currentBase.value = id
  page.value = 1
  void loadChunks()
}

function removeChunk(chunk: KnowledgeChunk) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await contentApi.deleteKnowledgeChunk(chunk.id, token)
    MessagePlugin.success('分块已删除')
    await loadChunks()
  }
}

function syncAll() {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    syncing.value = true
    try {
      await contentApi.syncKnowledge(currentBase.value, token)
      MessagePlugin.success('知识库已重新同步')
      await loadBases()
      await loadChunks()
    } finally {
      syncing.value = false
    }
  }
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

function formatTime(s: string): string {
  return new Date(s).toLocaleString()
}

onMounted(() => {
  void loadBases()
  void loadChunks()
})
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">知识库</h2>
      <t-button v-if="auth.isSuper" theme="primary" variant="outline" :loading="syncing" @click="syncAll">
        <template #icon><RefreshCw :size="16" /></template>
        重新同步
      </t-button>
    </div>

    <!-- 知识库卡片 -->
    <t-row :gutter="16" class="mb-16">
      <t-col v-for="b in bases" :key="b.id" :xs="12" :sm="8" :md="6" :lg="4">
        <t-card
          class="base-card"
          :class="{ active: currentBase === b.id }"
          @click="selectBase(b.id)"
        >
          <div class="base-name">{{ b.name }}</div>
          <div class="base-id">{{ b.id }} · {{ b.provider }}</div>
          <div class="base-desc">{{ b.description || '暂无描述' }}</div>
          <div class="base-meta">
            <t-tag :theme="b.enabled ? 'success' : 'default'" variant="light" size="small">
              {{ b.enabled ? '启用' : '停用' }}
            </t-tag>
            <t-tag theme="primary" variant="light" size="small">{{ b.chunks }} 分块</t-tag>
          </div>
        </t-card>
      </t-col>
    </t-row>

    <!-- 分块列表 -->
    <t-card title="知识分块">
      <t-space class="mb-16">
        <t-select
          v-model="currentBase"
          placeholder="全部知识库"
          clearable
          :options="bases.map((b) => ({ value: b.id, label: b.name }))"
          :style="{ width: 200 }"
          @change="selectBase"
        />
        <t-input
          v-model="keyword"
          placeholder="搜索标题 / 内容…"
          clearable
          :style="{ width: 240 }"
          @enter="search"
          @clear="search"
        />
        <t-button theme="primary" :loading="loading" @click="search">查询</t-button>
      </t-space>

      <t-table
        :data="chunks"
        :loading="loading"
        row-key="id"
        :pagination="{ current: page, pageSize, total: chunksTotal, onChange: pageChange }"
        :columns="columns"
      >
        <template #id="{ row }">{{ row.id }}</template>
        <template #title="{ row }">{{ row.title || '（无标题）' }}</template>
        <template #content="{ row }">
          <div class="chunk-content">{{ row.content }}</div>
        </template>
        <template #source_id="{ row }">
          <t-tooltip :content="row.source_id" placement="top" show-arrow>
            <span class="source">{{ row.source_id }}</span>
          </t-tooltip>
        </template>
        <template #created_at="{ row }">{{ formatTime(row.updated_at) }}</template>
        <template #ops="{ row }">
          <t-button v-if="auth.isSuper" size="small" variant="base" theme="danger" @click="removeChunk(row)">
            <template #icon><Trash2 :size="14" /></template>删除
          </t-button>
        </template>
      </t-table>
    </t-card>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.base-card {
  cursor: pointer;
  transition: border-color 0.2s;
  border: 2px solid transparent;
}
.base-card.active {
  border-color: var(--td-brand-color);
}
.base-name {
  font-weight: 600;
  font-size: 15px;
}
.base-id {
  font-size: 12px;
  color: var(--mgr-text-secondary);
  margin: 4px 0;
}
.base-desc {
  font-size: 13px;
  color: var(--mgr-text-secondary);
  min-height: 36px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.base-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
}
.chunk-content {
  max-width: 480px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.source {
  display: inline-block;
  max-width: 180px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
}
</style>
