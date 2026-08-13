<script setup lang="ts">
// 通用占位视图：展示待实现模块的标题与说明（后续里程碑逐项实现）
import { useRoute } from 'vue-router'
import { Construction } from 'lucide-vue-next'

const route = useRoute()
const title = String(route.meta.title ?? '未命名模块')
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">{{ title }}</h2>
    </div>
    <t-empty>
      <template #description>
        <div class="empty-body">
          <Construction :size="40" />
          <p>「{{ title }}」模块正在建设中，将在后续里程碑上线。</p>
        </div>
      </template>
    </t-empty>
  </div>
</template>

<style scoped>
.empty-body {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  color: var(--mgr-text-secondary);
}
</style>
