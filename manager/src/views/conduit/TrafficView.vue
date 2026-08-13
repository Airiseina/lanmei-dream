<script setup lang="ts">
// 节点流量：按管线/节点维度查看经过的流量（计数/错误/耗时），ECharts 柱状图
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import * as echarts from 'echarts'
import { conduitApi } from '@/api'
import { useAppStore } from '@/stores/app'
import type { NodeTraffic } from '@/types/api'
import type { PrimaryTableCol } from 'tdesign-vue-next'

const app = useAppStore()
const isDark = computed(() => app.theme === 'dark')

const columns: PrimaryTableCol[] = [
  { colKey: 'bucket', title: '时间桶' },
  { colKey: 'pipeline_id', title: '管线' },
  { colKey: 'node_name', title: '节点' },
  { colKey: 'count', title: '流量', align: 'right' },
  { colKey: 'error_count', title: '错误', align: 'right' },
  { colKey: 'avg', title: '平均耗时', align: 'right' },
]

const items = ref<NodeTraffic[]>([])
const loading = ref(false)
const filters = reactive({ pipeline: '', node: '', days: 1 })

const chartRef = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null

async function load() {
  loading.value = true
  try {
    const since = new Date(Date.now() - filters.days * 24 * 3600 * 1000).toISOString()
    const res = await conduitApi.traffic(
      filters.pipeline || undefined,
      filters.node || undefined,
      since,
      undefined,
    )
    items.value = res.items
    await nextTick()
    renderChart()
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  } finally {
    loading.value = false
  }
}

function renderChart() {
  if (!chartRef.value) return
  if (!chart) chart = echarts.init(chartRef.value)
  // 按 pipeline+node 聚合
  const agg = new Map<string, { count: number; err: number; dur: number }>()
  for (const it of items.value) {
    const key = `${it.pipeline_id}/${it.node_name}`
    const cur = agg.get(key) ?? { count: 0, err: 0, dur: 0 }
    cur.count += it.count
    cur.err += it.error_count
    cur.dur += it.total_duration_ms
    agg.set(key, cur)
  }
  const keys = [...agg.keys()]
  // 图表配色随主题联动（dark 下浅色文字/分隔，避免黑底黑字看不清）
  const textColor = isDark.value ? '#a8abb3' : '#6e6e75'
  const axisColor = isDark.value ? '#3a3a3e' : '#e5e6eb'
  const splitColor = isDark.value ? '#2c2c30' : '#f0f0f2'
  chart.setOption({
    backgroundColor: 'transparent',
    tooltip: {
      trigger: 'axis',
      backgroundColor: isDark.value ? '#2a2a2d' : '#ffffff',
      borderColor: isDark.value ? '#3a3a3e' : '#e3e3e6',
      textStyle: { color: textColor },
    },
    legend: { data: ['流量', '错误'], textStyle: { color: textColor } },
    grid: { left: 48, right: 24, top: 40, bottom: 64 },
    xAxis: {
      type: 'category',
      data: keys,
      axisLabel: { rotate: 30, color: textColor },
      axisLine: { lineStyle: { color: axisColor } },
    },
    yAxis: {
      type: 'value',
      name: '次数',
      nameTextStyle: { color: textColor },
      axisLine: { lineStyle: { color: axisColor } },
      axisLabel: { color: textColor },
      splitLine: { lineStyle: { color: splitColor } },
    },
    series: [
      {
        name: '流量',
        type: 'bar',
        barMaxWidth: 28,
        data: keys.map((k) => agg.get(k)!.count),
      },
      {
        name: '错误',
        type: 'bar',
        barMaxWidth: 28,
        data: keys.map((k) => agg.get(k)!.err),
      },
    ],
  })
}

function handleResize() {
  chart?.resize()
}

watch(() => [filters.pipeline, filters.node, filters.days], load)
// 主题切换时重绘图表，保证 dark 配色立即生效
watch(isDark, () => {
  if (chart) renderChart()
})
onMounted(() => {
  load()
  window.addEventListener('resize', handleResize)
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', handleResize)
  chart?.dispose()
  chart = null
})
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">节点流量</h2>
    </div>

    <t-card class="mb-16">
      <t-space break-line>
        <t-input v-model="filters.pipeline" placeholder="管线 ID（留空全部）" clearable :style="{ width: 200 }" />
        <t-input v-model="filters.node" placeholder="节点名（留空全部）" clearable :style="{ width: 200 }" />
        <t-select
          v-model="filters.days"
          :options="[{ value: 1, label: '近 1 天' }, { value: 7, label: '近 7 天' }, { value: 30, label: '近 30 天' }]"
          :style="{ width: 120 }"
        />
        <t-button theme="primary" :loading="loading" @click="load">查询</t-button>
      </t-space>
    </t-card>

    <t-card title="节点流量分布" class="mb-16">
      <div ref="chartRef" class="chart" />
    </t-card>

    <t-card title="明细">
      <t-table :data="items" row-key="id" :loading="loading" :columns="columns">
        <template #bucket="{ row }">{{ new Date(row.bucket).toLocaleString() }}</template>
        <template #pipeline_id="{ row }">
          <t-tag variant="light" theme="primary">{{ row.pipeline_id }}</t-tag>
        </template>
        <template #node_name="{ row }">{{ row.node_name }}</template>
        <template #count="{ row }">{{ row.count }}</template>
        <template #error_count="{ row }">
          <span :class="{ 'err-text': row.error_count > 0 }">{{ row.error_count }}</span>
        </template>
        <template #avg="{ row }">
          {{ row.count > 0 ? `${Math.round(row.total_duration_ms / row.count)}ms` : '—' }}
        </template>
      </t-table>
    </t-card>
  </div>
</template>

<style scoped>
.mb-16 {
  margin-bottom: 16px;
}
.chart {
  height: 340px;
  width: 100%;
}
.err-text {
  color: var(--td-error-color);
  font-weight: 600;
}
</style>
