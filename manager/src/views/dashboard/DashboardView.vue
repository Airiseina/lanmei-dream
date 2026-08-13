<script setup lang="ts">
// 仪表盘：今日运行概览 + 运行时状态（ECharts 图表在后续里程碑补充）
import { onMounted, ref } from 'vue'
import { AlertTriangle, Coins, MessageSquare, Radio, Zap } from 'lucide-vue-next'
import { dashboardApi } from '@/api'
import type { DashboardStats } from '@/types/api'

const stats = ref<DashboardStats | null>(null)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    stats.value = await dashboardApi.stats()
  } catch (err) {
    error.value = err instanceof Error ? err.message : '加载失败'
  } finally {
    loading.value = false
  }
}

onMounted(load)

function formatCents(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`
}
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">仪表盘</h2>
      <t-button theme="primary" variant="outline" :loading="loading" @click="load">刷新</t-button>
    </div>

    <t-alert v-if="error" theme="error" :message="error" class="mb-16" />

    <t-skeleton v-if="!stats && loading" :row="4" />

    <template v-else-if="stats">
      <!-- 今日统计卡片 -->
      <t-row :gutter="[16, 16]">
        <t-col :xs="12" :sm="6" :lg="3">
          <t-card class="stat-card">
            <div class="stat-icon"><MessageSquare :size="22" /></div>
            <div class="stat-meta">
              <div class="stat-value">{{ stats.today.messages_processed }}</div>
              <div class="stat-label">今日处理消息</div>
            </div>
          </t-card>
        </t-col>
        <t-col :xs="12" :sm="6" :lg="3">
          <t-card class="stat-card">
            <div class="stat-icon error"><AlertTriangle :size="22" /></div>
            <div class="stat-meta">
              <div class="stat-value">{{ stats.today.messages_error }}</div>
              <div class="stat-label">今日错误</div>
            </div>
          </t-card>
        </t-col>
        <t-col :xs="12" :sm="6" :lg="3">
          <t-card class="stat-card">
            <div class="stat-icon llm"><Zap :size="22" /></div>
            <div class="stat-meta">
              <div class="stat-value">{{ stats.today.llm_calls }}</div>
              <div class="stat-label">今日 LLM 调用</div>
            </div>
          </t-card>
        </t-col>
        <t-col :xs="12" :sm="6" :lg="3">
          <t-card class="stat-card">
            <div class="stat-icon coin"><Coins :size="22" /></div>
            <div class="stat-meta">
              <div class="stat-value">{{ formatCents(stats.today.cost_cents) }}</div>
              <div class="stat-label">今日费用</div>
            </div>
          </t-card>
        </t-col>
      </t-row>

      <!-- 运行时状态 -->
      <t-card title="运行时状态" class="mt-16">
        <t-descriptions :column="3" bordered>
          <t-descriptions-item label="活跃 Provider">
            <t-tag theme="primary" variant="light">{{ stats.runtime.active_provider || '未配置' }}</t-tag>
          </t-descriptions-item>
          <t-descriptions-item label="活跃模型">{{ stats.runtime.active_model || '—' }}</t-descriptions-item>
          <t-descriptions-item label="Provider 数量">{{ stats.runtime.provider_count }}</t-descriptions-item>
          <t-descriptions-item label="插件数量">{{ stats.runtime.plugin_count }}</t-descriptions-item>
          <t-descriptions-item label="管理员数量">{{ stats.runtime.admin_count }}</t-descriptions-item>
          <t-descriptions-item label="引擎状态">
            <t-tag :theme="stats.runtime.engine_running ? 'success' : 'danger'" variant="light">
              {{ stats.runtime.engine_running ? '运行中' : '已停止' }}
            </t-tag>
          </t-descriptions-item>
          <t-descriptions-item label="消息队列深度">
            <t-tag variant="light" theme="warning"><Radio :size="14" /> {{ stats.runtime.queue_len }}</t-tag>
          </t-descriptions-item>
        </t-descriptions>
      </t-card>
    </template>
  </div>
</template>

<style scoped>
.mb-16 {
  margin-bottom: 16px;
}
.mt-16 {
  margin-top: 16px;
}

.stat-card :deep(.t-card__body) {
  display: flex;
  align-items: center;
  gap: 14px;
}

.stat-icon {
  width: 44px;
  height: 44px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #1890ff18;
  color: #1890ff;
  flex-shrink: 0;
}

.stat-icon.error {
  background: #e34d5918;
  color: #e34d59;
}

.stat-icon.llm {
  background: #722ed118;
  color: #722ed1;
}

.stat-icon.coin {
  background: #f7b50018;
  color: #f7b500;
}

.stat-value {
  font-size: 22px;
  font-weight: 600;
  line-height: 1.2;
}

.stat-label {
  font-size: 12px;
  color: var(--mgr-text-secondary);
}
</style>
