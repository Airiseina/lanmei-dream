<script setup lang="ts">
// 底部悬浮"变更保存"条：仅在存在未保存变更时出现。
// 含变更说明输入（留痕）、放弃、应用（二次验证在父组件内完成）。
import { ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { RotateCcw, Save } from 'lucide-vue-next'

defineProps<{
  visible: boolean
  saving?: boolean
  placeholder?: string
}>()

const emit = defineEmits<{
  (e: 'save', comment: string): void
  (e: 'cancel'): void
}>()

const comment = ref('')

function doSave() {
  if (!comment.value.trim()) {
    MessagePlugin.warning('请填写变更说明（留痕）')
    return
  }
  emit('save', comment.value.trim())
  comment.value = ''
}
</script>

<template>
  <transition name="savebar">
    <div v-if="visible" class="save-bar">
      <t-input
        v-model="comment"
        :placeholder="placeholder ?? '变更说明（必填，将记入审计）'"
        maxlength="200"
        :style="{ flex: 1, minWidth: 160 }"
      />
      <t-button variant="outline" @click="emit('cancel')">
        <template #icon><RotateCcw :size="16" /></template>
        放弃
      </t-button>
      <t-button theme="primary" :loading="saving" @click="doSave">
        <template #icon><Save :size="16" /></template>
        应用变更
      </t-button>
    </div>
  </transition>
</template>

<style scoped>
.save-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 16px;
  background: var(--mgr-bg-card);
  border: 1px solid var(--mgr-border);
  border-radius: var(--mgr-radius);
  box-shadow: var(--mgr-shadow-lg);
}

/* 底部悬浮条入场动画 */
.savebar-enter-active,
.savebar-leave-active {
  transition: transform 0.25s ease, opacity 0.25s ease;
}
.savebar-enter-from,
.savebar-leave-to {
  transform: translateY(16px);
  opacity: 0;
}
</style>
