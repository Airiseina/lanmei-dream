<script setup lang="ts">
// 插件管理：内置插件与 Wasm 插件统一列表，支持启停（Wasm 持久化/内置运行时）+ 卸载
import { onMounted, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { Power, Trash2 } from 'lucide-vue-next'
import { contentApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { PluginView } from '@/types/api'

const auth = useAuthStore()

const items = ref<PluginView[]>([])
const loading = ref(false)

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

async function load() {
  loading.value = true
  try {
    const res = await contentApi.plugins()
    items.value = res.items
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  } finally {
    loading.value = false
  }
}

function toggle(p: PluginView) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    if (p.enabled) {
      await contentApi.disablePlugin(p.plugin_id, token)
      MessagePlugin.success(`已停用「${p.name}」`)
    } else {
      await contentApi.enablePlugin(p.plugin_id, token)
      MessagePlugin.success(`已启用「${p.name}」`)
    }
    await load()
  }
}

function remove(p: PluginView) {
  const confirm = DialogPlugin.confirm({
    header: '卸载插件',
    body: `确定卸载 Wasm 插件「${p.name}」吗？将删除安装记录与插件文件。`,
    theme: 'danger',
    confirmBtn: { content: '卸载', theme: 'danger' },
    onConfirm: async () => {
      stepUpVisible.value = true
      pendingAction.value = async (token) => {
        await contentApi.deletePlugin(p.plugin_id, token)
        MessagePlugin.success('插件已卸载')
        confirm.destroy()
        await load()
      }
    },
  })
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

function stateTag(p: PluginView) {
  if (p.load_error) return { theme: 'danger' as const, text: '加载失败' }
  if (p.enabled) return { theme: 'success' as const, text: '已启用' }
  return { theme: 'default' as const, text: '已停用' }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">插件</h2>
      <t-button theme="primary" variant="outline" :loading="loading" @click="load">刷新</t-button>
    </div>

    <t-row :gutter="16">
      <t-col v-for="p in items" :key="p.plugin_id" :xs="24" :sm="12" :md="8" :lg="6">
        <t-card class="plugin-card">
          <div class="plugin-head">
            <div class="plugin-title">
              <span class="plugin-name">{{ p.name }}</span>
              <t-tag v-if="p.version" theme="default" variant="light" size="small">v{{ p.version }}</t-tag>
              <t-tag :theme="p.kind === 'wasm' ? 'warning' : 'primary'" variant="light" size="small">
                {{ p.kind === 'wasm' ? 'Wasm' : '内置' }}
              </t-tag>
            </div>
            <t-tag :theme="stateTag(p).theme" variant="light" size="small">{{ stateTag(p).text }}</t-tag>
          </div>
          <div class="plugin-id">{{ p.plugin_id }}</div>
          <div class="plugin-desc">{{ p.description || '暂无描述' }}</div>

          <t-tag v-if="p.load_error" theme="danger" variant="outline" class="plugin-err" size="small">
            {{ p.load_error }}
          </t-tag>

          <div v-if="p.commands.length || p.tools.length" class="plugin-tags">
            <t-tag v-for="cmd in p.commands" :key="'c' + cmd" theme="success" variant="light" size="small">
              /{{ cmd }}
            </t-tag>
            <t-tag v-for="tool in p.tools" :key="'t' + tool" theme="info" variant="light" size="small">
              {{ tool }}
            </t-tag>
          </div>

          <div v-if="auth.isSuper" class="plugin-ops">
            <t-button size="small" variant="base" :theme="p.enabled ? 'danger' : 'primary'" @click="toggle(p)">
              <template #icon><Power :size="14" /></template>
              {{ p.enabled ? '停用' : '启用' }}
            </t-button>
            <t-button v-if="p.kind === 'wasm'" size="small" variant="base" theme="danger" @click="remove(p)">
              <template #icon><Trash2 :size="14" /></template>卸载
            </t-button>
          </div>
        </t-card>
      </t-col>
    </t-row>

    <t-empty v-if="!loading && items.length === 0" description="暂无插件" />

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.plugin-card {
  margin-bottom: 16px;
}
.plugin-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.plugin-title {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.plugin-name {
  font-size: 16px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.plugin-id {
  font-size: 12px;
  color: var(--mgr-text-muted);
  margin: 4px 0 2px;
  word-break: break-all;
}
.plugin-desc {
  font-size: 13px;
  color: var(--mgr-text-secondary);
  min-height: 36px;
}
.plugin-err {
  display: block;
  margin: 8px 0;
  white-space: normal;
}
.plugin-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 8px 0;
}
.plugin-ops {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--mgr-border);
}
</style>
