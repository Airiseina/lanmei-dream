<script setup lang="ts">
// 命令管理：只读展示内置命令与插件注册命令（数据源为运行时命令系统）
import { onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol } from 'tdesign-vue-next'
import { contentApi } from '@/api'
import type { CommandView } from '@/types/api'

const columns: PrimaryTableCol[] = [
  { colKey: 'name', title: '命令', width: 220 },
  { colKey: 'description', title: '描述' },
  { colKey: 'source', title: '来源', width: 180 },
]

const items = ref<CommandView[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const res = await contentApi.commands()
    items.value = res.items
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">命令</h2>
      <t-button theme="primary" variant="outline" :loading="loading" @click="load">刷新</t-button>
    </div>

    <t-alert
      theme="info"
      message="命令列表来自运行时注册表（内置命令 + 插件命令），由插件/意图分析使用。"
      class="mb-16"
    />

    <t-table :data="items" :loading="loading" row-key="name" :columns="columns">
      <template #name="{ row }">
        <t-tag theme="success" variant="light">/{{ row.name }}</t-tag>
      </template>
      <template #description="{ row }">{{ row.description || '—' }}</template>
      <template #source="{ row }">
        <t-tag :theme="row.source.startsWith('plugin:') ? 'primary' : 'default'" variant="light">
          {{ row.source.startsWith('plugin:') ? row.source.slice('plugin:'.length) : '内置' }}
        </t-tag>
      </template>
    </t-table>

    <t-empty v-if="!loading && items.length === 0" description="暂无命令" />
  </div>
</template>
