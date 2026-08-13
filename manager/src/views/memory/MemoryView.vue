<script setup lang="ts">
// 记忆管理：按用户/群/关键字浏览长期记忆，支持删除（需 super + step-up）
import { onMounted, reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol } from 'tdesign-vue-next'
import { Trash2 } from 'lucide-vue-next'
import { contentApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { MemoryView } from '@/types/api'

const columns: PrimaryTableCol[] = [
  { colKey: 'id', title: 'ID', width: 80, align: 'center' },
  { colKey: 'user_id', title: '用户 ID', width: 120 },
  { colKey: 'group_id', title: '群 ID', width: 180 },
  { colKey: 'content', title: '记忆内容' },
  { colKey: 'created_at', title: '记录时间', width: 180 },
  { colKey: 'ops', title: '操作', width: 90, fixed: 'right' },
]

const auth = useAuthStore()

const items = ref<MemoryView[]>([])
const total = ref(0)
const loading = ref(false)
const page = ref(1)
const pageSize = 20
const filters = reactive({ user_id: '', group_id: '', keyword: '' })

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

async function load() {
  loading.value = true
  try {
    const res = await contentApi.memories({
      userId: filters.user_id || undefined,
      groupId: filters.group_id || undefined,
      keyword: filters.keyword || undefined,
      page: page.value,
      pageSize,
    })
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

function remove(m: MemoryView) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await contentApi.deleteMemory(m.id, token)
    MessagePlugin.success('记忆已删除')
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
      <h2 class="page-title">记忆</h2>
      <t-space>
        <t-input v-model="filters.user_id" placeholder="用户 ID" clearable :style="{ width: 120 }" @enter="search" />
        <t-input v-model="filters.group_id" placeholder="群 ID" clearable :style="{ width: 160 }" @enter="search" />
        <t-input v-model="filters.keyword" placeholder="搜索记忆内容…" clearable :style="{ width: 200 }" @enter="search" />
        <t-button theme="primary" :loading="loading" @click="search">查询</t-button>
      </t-space>
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
        <template #user_id="{ row }">
          <span v-if="row.user_id === 0" class="muted">群级</span>
          <span v-else>{{ row.user_id }}</span>
        </template>
        <template #group_id="{ row }">
          <span v-if="!row.group_id" class="muted">私聊</span>
          <span v-else>{{ row.group_id }}</span>
        </template>
        <template #content="{ row }">
          <div class="mem-content">{{ row.content }}</div>
        </template>
        <template #created_at="{ row }">{{ formatTime(row.created_at) }}</template>
        <template #ops="{ row }">
          <t-button v-if="auth.isSuper" size="small" variant="base" theme="danger" @click="remove(row)">
            <template #icon><Trash2 :size="14" /></template>删除
          </t-button>
        </template>
      </t-table>
    </t-card>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.mem-content {
  max-width: 520px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.muted {
  color: var(--mgr-text-secondary);
}
</style>
