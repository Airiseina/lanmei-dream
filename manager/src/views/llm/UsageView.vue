<script setup lang="ts">
// Token 用量与计费统计：维度汇总表 + 时间序列图（ECharts）
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import * as echarts from 'echarts'
import { llmApi } from '@/api'
import { useAppStore } from '@/stores/app'
import type { UsagePoint, UsageSummaryRow } from '@/types/api'
import type { PrimaryTableCol } from 'tdesign-vue-next'

const app = useAppStore()
const isDark = computed(() => app.theme === 'dark')

const columns: PrimaryTableCol[] = [
  { colKey: 'dimension', title: '维度值' },
  { colKey: 'total', title: '总 Token' },
  { colKey: 'input', title: '输入 Token' },
  { colKey: 'output', title: '输出 Token' },
  { colKey: 'cost', title: '费用' },
]

const by = ref('model')
const step = ref('hour')
const days = ref(1)

const summary = ref<UsageSummaryRow[]>([])
const series = ref<UsagePoint[]>([])
const loading = ref(false)

const chartRef = ref<HTMLDivElement>()
let chart: echarts.ECharts | null = null

const since = computed(() => {
  const d = new Date(Date.now() - days.value * 24 * 3600 * 1000)
  return d.toISOString()
})

const dimOptions = [
  { value: 'model', label: '模型' },
  { value: 'provider', label: 'Provider' },
  { value: 'scene', label: '场景' },
  { value: 'platform', label: '平台' },
  { value: 'user_id', label: '用户' },
  { value: 'group_id', label: '群' },
]

async function load() {
  loading.value = true
  try {
    const [sumRes, seriesRes] = await Promise.all([
      llmApi.usageSummary(by.value, since.value),
      llmApi.usageSeries(step.value, since.value),
    ])
    summary.value = sumRes.items ?? []
    series.value = seriesRes.items ?? []
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
    legend: { data: ['Token 消耗', '费用（分）'], textStyle: { color: textColor } },
    grid: { left: 48, right: 24, top: 40, bottom: 32 },
    xAxis: {
      type: 'category',
      data: series.value.map((p) => new Date(p.ts).toLocaleString()),
      axisLine: { lineStyle: { color: axisColor } },
      axisLabel: { color: textColor },
    },
    yAxis: [
      {
        type: 'value',
        name: 'Token',
        nameTextStyle: { color: textColor },
        axisLine: { lineStyle: { color: axisColor } },
        axisLabel: { color: textColor },
        splitLine: { lineStyle: { color: splitColor } },
      },
      {
        type: 'value',
        name: '费用(分)',
        nameTextStyle: { color: textColor },
        axisLine: { lineStyle: { color: axisColor } },
        axisLabel: { color: textColor },
        splitLine: { lineStyle: { color: splitColor } },
      },
    ],
    series: [
      {
        name: 'Token 消耗',
        type: 'line',
        smooth: true,
        areaStyle: { opacity: 0.15 },
        data: series.value.map((p) => p.total_tokens),
      },
      {
        name: '费用（分）',
        type: 'line',
        yAxisIndex: 1,
        smooth: true,
        data: series.value.map((p) => p.cost_cents),
      },
    ],
  })
}

function fmtTokens(n: number): string {
  return n.toLocaleString()
}

function fmtCost(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`
}

function handleResize() {
  chart?.resize()
}

watch([by, step, days], load)
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
      <h2 class="page-title">Token 用量</h2>
      <div class="filters">
        <t-select v-model="step" :options="[{ value: 'hour', label: '按小时' }, { value: 'day', label: '按天' }, { value: 'minute', label: '按分钟' }]" :style="{ width: 120 }" />
        <t-select v-model="days" :options="[{ value: 1, label: '近 1 天' }, { value: 7, label: '近 7 天' }, { value: 30, label: '近 30 天' }]" :style="{ width: 120 }" />
        <t-button theme="primary" :loading="loading" @click="load">查询</t-button>
      </div>
    </div>

    <t-card title="用量趋势" class="mb-16">
      <div ref="chartRef" class="chart" />
    </t-card>

    <t-card title="维度汇总">
      <t-space class="mb-16" break-line>
        <t-radio-group v-model="by" variant="default-filled">
          <t-radio-button v-for="opt in dimOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</t-radio-button>
        </t-radio-group>
      </t-space>
      <t-table :data="summary" row-key="dimension" :loading="loading" :columns="columns">
        <template #dimension="{ row }">{{ row.dimension || '（空）' }}</template>
        <template #total="{ row }">{{ fmtTokens(row.total_tokens) }}</template>
        <template #input="{ row }">{{ fmtTokens(row.input_tokens) }}</template>
        <template #output="{ row }">{{ fmtTokens(row.output_tokens) }}</template>
        <template #cost="{ row }">{{ fmtCost(row.cost_cents) }}</template>
      </t-table>
    </t-card>
  </div>
</template>

<style scoped>
.filters {
  display: flex;
  gap: 8px;
}
.mb-16 {
  margin-bottom: 16px;
}
.chart {
  height: 320px;
  width: 100%;
}
</style>
