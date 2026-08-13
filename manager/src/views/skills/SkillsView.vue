<script setup lang="ts">
// Skills 管理：技能列表 + 运行时启停（写回 skills.toml，需 super + step-up）
import { onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { Power } from 'lucide-vue-next'
import { contentApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { SkillView } from '@/types/api'

const auth = useAuthStore()

const items = ref<SkillView[]>([])
const loading = ref(false)

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

async function load() {
  loading.value = true
  try {
    const res = await contentApi.skills()
    items.value = res.items
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  } finally {
    loading.value = false
  }
}

function toggle(s: SkillView) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    if (s.enabled) {
      await contentApi.disableSkill(s.id, token)
      MessagePlugin.success(`已停用「${s.name}」`)
    } else {
      await contentApi.enableSkill(s.id, token)
      MessagePlugin.success(`已启用「${s.name}」`)
    }
    await load()
  }
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">Skills</h2>
      <t-button theme="primary" variant="outline" :loading="loading" @click="load">刷新</t-button>
    </div>

    <t-row :gutter="16">
      <t-col v-for="s in items" :key="s.id" :xs="24" :sm="12" :md="8" :lg="6">
        <t-card class="skill-card">
          <div class="skill-head">
            <div class="skill-name">
              {{ s.name }}
              <t-tag v-if="s.version" theme="default" variant="light" size="small">v{{ s.version }}</t-tag>
            </div>
            <t-tag :theme="s.enabled ? 'success' : 'default'" variant="light" size="small">
              {{ s.enabled ? '已启用' : '已停用' }}
            </t-tag>
          </div>
          <div class="skill-id">{{ s.id }}<span v-if="s.author"> · {{ s.author }}</span></div>
          <div class="skill-desc">{{ s.description || '暂无描述' }}</div>
          <div v-if="s.tags.length" class="skill-tags">
            <t-tag v-for="tag in s.tags" :key="tag" theme="info" variant="light" size="small">{{ tag }}</t-tag>
          </div>
          <div class="skill-meta">
            {{ s.content_len }} 字符 · {{ s.dir }}
          </div>
          <div v-if="auth.isSuper" class="skill-ops">
            <t-button size="small" variant="base" :theme="s.enabled ? 'danger' : 'primary'" @click="toggle(s)">
              <template #icon><Power :size="14" /></template>
              {{ s.enabled ? '停用' : '启用' }}
            </t-button>
          </div>
        </t-card>
      </t-col>
    </t-row>

    <t-empty v-if="!loading && items.length === 0" description="暂无 Skills" />

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.skill-card {
  margin-bottom: 16px;
}
.skill-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.skill-name {
  font-size: 16px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.skill-id {
  font-size: 12px;
  color: var(--mgr-text-secondary);
  margin: 4px 0;
}
.skill-desc {
  font-size: 13px;
  color: var(--mgr-text-secondary);
  min-height: 36px;
}
.skill-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin: 8px 0;
}
.skill-meta {
  font-size: 12px;
  color: var(--mgr-text-secondary);
}
.skill-ops {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
  padding-top: 10px;
  border-top: 1px solid var(--mgr-border);
}
</style>
