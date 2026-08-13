<script setup lang="ts">
// 操作审计：全量留痕查询（零信任设计的审计基线）
import { onMounted, reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { auditApi } from '@/api'
import type { AuditLog } from '@/types/api'
import type { PrimaryTableCol } from 'tdesign-vue-next'

const columns: PrimaryTableCol[] = [
  { colKey: 'created_at', title: '时间', width: 170 },
  { colKey: 'username', title: '操作人', width: 110 },
  { colKey: 'action', title: '操作', width: 180 },
  { colKey: 'target', title: '目标', width: 160 },
  { colKey: 'detail', title: '详情' },
  { colKey: 'ip', title: 'IP', width: 120 },
  { colKey: 'result', title: '结果', width: 80, align: 'center' },
]

const items = ref<AuditLog[]>([])
const total = ref(0)
const loading = ref(false)
const page = ref(1)
const pageSize = 20

const filters = reactive({ action: '', username: '', result: '' })

const resultMeta: Record<string, { theme: string; label: string }> = {
  ok: { theme: 'success', label: '成功' },
  deny: { theme: 'warning', label: '拒绝' },
  error: { theme: 'danger', label: '失败' },
}

async function load() {
  loading.value = true
  try {
    const res = await auditApi.list({
      action: filters.action || undefined,
      username: filters.username || undefined,
      result: filters.result || undefined,
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

function formatTime(s: string): string {
  return new Date(s).toLocaleString()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">操作审计</h2>
    </div>

    <t-card class="mb-16">
      <t-space break-line>
        <t-input v-model="filters.action" placeholder="操作（如 admin.create）" clearable :style="{ width: 200 }" />
        <t-input v-model="filters.username" placeholder="操作人" clearable :style="{ width: 140 }" />
        <t-select
          v-model="filters.result"
          placeholder="结果"
          clearable
          :options="[
            { value: 'ok', label: '成功' },
            { value: 'deny', label: '拒绝' },
            { value: 'error', label: '失败' },
          ]"
          :style="{ width: 120 }"
        />
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
        <template #created_at="{ row }">{{ formatTime(row.created_at) }}</template>
        <template #username="{ row }">{{ row.username || '系统' }}</template>
        <template #action="{ row }">
          <span class="mono">{{ row.action }}</span>
        </template>
        <template #target="{ row }">
          <span class="mono">{{ row.target_type }}:{{ row.target_id }}</span>
        </template>
        <template #detail="{ row }">
          <t-tooltip v-if="row.detail" :content="row.detail" placement="top" theme="light">
            <span class="detail-ellipsis">{{ row.detail }}</span>
          </t-tooltip>
          <span v-else>—</span>
        </template>
        <template #ip="{ row }">{{ row.ip || '—' }}</template>
        <template #result="{ row }">
          <t-tag :theme="resultMeta[row.result]?.theme ?? 'default'" variant="light">
            {{ resultMeta[row.result]?.label ?? row.result }}
          </t-tag>
        </template>
      </t-table>
    </t-card>
  </div>
</template>

<style scoped>
.mb-16 {
  margin-bottom: 16px;
}
.mono {
  font-family: 'SFMono-Regular', Consolas, monospace;
  font-size: 12px;
}
.detail-ellipsis {
  display: inline-block;
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: middle;
}
</style>
