<script setup lang="ts">
// Trace 审计：执行链路查询 + SSE 实时推送（可查看节点状态/耗时/错误）
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { Activity, Radio } from 'lucide-vue-next'
import { conduitApi } from '@/api'
import type { ConduitTrace } from '@/types/api'
import type { PrimaryTableCol } from 'tdesign-vue-next'

const columns: PrimaryTableCol[] = [
  { colKey: 'message_id', title: '消息', width: 180 },
  { colKey: 'group_id', title: '群', width: 100 },
  { colKey: 'platform', title: '平台', width: 80 },
  { colKey: 'pipeline', title: '管线', width: 150 },
  { colKey: 'status', title: '状态', width: 80, align: 'center' },
  { colKey: 'duration', title: '耗时', width: 90 },
  { colKey: 'created_at', title: '时间', width: 170 },
  { colKey: 'ops', title: '操作', width: 90, fixed: 'right' },
]

const items = ref<ConduitTrace[]>([])
const total = ref(0)
const loading = ref(false)
const page = ref(1)
const pageSize = 20

const filters = reactive({ pipeline: '', status: '', groupId: '' })

const realtime = ref(false)
let closeStream: (() => void) | null = null

const detailVisible = ref(false)
const detail = ref<ConduitTrace | null>(null)

const pipelines = ref<string[]>([])

async function load() {
  loading.value = true
  try {
    const res = await conduitApi.traces({
      pipeline: filters.pipeline || undefined,
      status: filters.status || undefined,
      groupId: filters.groupId || undefined,
      page: page.value,
      pageSize,
    })
    items.value = res.items
    total.value = res.total
    // 从结果汇总可选管线
    const set = new Set(pipelines.value)
    for (const it of res.items) if (it.pipeline) set.add(it.pipeline)
    pipelines.value = [...set]
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

function toggleRealtime() {
  if (realtime.value) {
    realtime.value = false
    closeStream?.()
    closeStream = null
  } else {
    try {
      closeStream = conduitApi.openTraceStream(
        (rec) => {
          // 新 Trace 插入列表顶部，去重并限制长度
          if (items.value.some((t) => t.trace_id === rec.trace_id && t.created_at === rec.created_at)) return
          items.value.unshift(rec)
          if (items.value.length > 100) items.value.pop()
        },
        (err) => {
          realtime.value = false
          MessagePlugin.warning(`实时推送中断：${err.message}`)
        },
      )
      realtime.value = true
    } catch (err) {
      MessagePlugin.error(err instanceof Error ? err.message : '无法连接实时推送')
    }
  }
}

function openDetail(t: ConduitTrace) {
  detail.value = t
  detailVisible.value = true
}

function formatTime(s: string): string {
  return new Date(s).toLocaleString()
}

function formatDur(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`
  return `${ms}ms`
}

const detailJson = computed(() => {
  return detail.value ? JSON.stringify(detail.value.trace, null, 2) : ''
})

onMounted(load)
onBeforeUnmount(() => closeStream?.())
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">Trace 审计</h2>
      <t-switch v-model="realtime" label="实时推送" @change="toggleRealtime">
        <template #label>
          <span class="realtime-label"><Radio :size="13" /> 实时</span>
        </template>
      </t-switch>
    </div>

    <t-card class="mb-16">
      <t-space break-line>
        <t-select
          v-model="filters.pipeline"
          placeholder="管线"
          clearable
          :options="pipelines.map((p) => ({ value: p, label: p }))"
          :style="{ width: 200 }"
        />
        <t-select
          v-model="filters.status"
          placeholder="状态"
          clearable
          :options="[
            { value: 'ok', label: '成功' },
            { value: 'error', label: '失败' },
          ]"
          :style="{ width: 140 }"
        />
        <t-input v-model="filters.groupId" placeholder="群 ID" clearable :style="{ width: 160 }" />
        <t-button theme="primary" :loading="loading" @click="load">查询</t-button>
      </t-space>
    </t-card>

    <t-card>
      <t-table
        :data="items"
        :loading="loading"
        row-key="id"
        :columns="columns"
        :pagination="{
          current: page,
          pageSize,
          total,
          onChange: pageChange,
        }"
      >
        <template #message_id="{ row }">
          <span class="mono">{{ row.message_id || row.trace_id }}</span>
        </template>
        <template #group_id="{ row }">{{ row.group_id || '—' }}</template>
        <template #platform="{ row }">{{ row.platform || '—' }}</template>
        <template #pipeline="{ row }">
          <t-tag variant="light" theme="primary">{{ row.pipeline || '—' }}</t-tag>
        </template>
        <template #status="{ row }">
          <t-tag :theme="row.status === 'ok' ? 'success' : 'danger'" variant="light">
            {{ row.status === 'ok' ? '成功' : '失败' }}
          </t-tag>
        </template>
        <template #duration="{ row }">{{ formatDur(row.duration_ms) }}</template>
        <template #created_at="{ row }">{{ formatTime(row.created_at) }}</template>
        <template #ops="{ row }">
          <t-button size="small" variant="base" @click="openDetail(row)"><template #icon><Activity :size="14" /></template>详情</t-button>
        </template>
      </t-table>
    </t-card>

    <t-drawer v-model:visible="detailVisible" header="Trace 详情" size="560px" :footer="null">
      <t-descriptions v-if="detail" :column="2" bordered class="mb-16">
        <t-descriptions-item label="Trace ID">
          <span class="mono">{{ detail.trace_id }}</span>
        </t-descriptions-item>
        <t-descriptions-item label="管线">{{ detail.pipeline || '—' }}</t-descriptions-item>
        <t-descriptions-item label="状态">
          <t-tag :theme="detail.status === 'ok' ? 'success' : 'danger'" variant="light">{{ detail.status }}</t-tag>
        </t-descriptions-item>
        <t-descriptions-item label="耗时">{{ formatDur(detail.duration_ms) }}</t-descriptions-item>
        <t-descriptions-item label="群">{{ detail.group_id || '—' }}</t-descriptions-item>
        <t-descriptions-item label="用户">{{ detail.user_id || '—' }}</t-descriptions-item>
      </t-descriptions>
      <t-alert v-if="detail?.err_msg" theme="error" :message="detail.err_msg" class="mb-16" />
      <pre class="trace-json">{{ detailJson }}</pre>
    </t-drawer>
  </div>
</template>

<style scoped>
.mb-16 {
  margin-bottom: 16px;
}
.mono {
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', monospace;
  font-size: 12px;
}
.realtime-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--td-brand-color);
}
.trace-json {
  max-height: 60vh;
  overflow: auto;
  background: var(--mgr-bg);
  border: 1px solid var(--mgr-border);
  border-radius: 6px;
  padding: 12px;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
