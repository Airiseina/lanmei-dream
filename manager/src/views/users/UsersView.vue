<script setup lang="ts">
// 用户管理：只读列表 + 封禁/解封（写操作需 super + step-up，封禁立即生效并审计留痕）
import { onMounted, reactive, ref } from 'vue'
import { MessagePlugin, DialogPlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol } from 'tdesign-vue-next'
import { contentApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { UserView } from '@/types/api'

const columns: PrimaryTableCol[] = [
  { colKey: 'id', title: 'ID', width: 80, align: 'center' },
  { colKey: 'nickname', title: '昵称' },
  { colKey: 'platform', title: '平台', width: 110 },
  { colKey: 'platform_user_id', title: '平台用户 ID' },
  { colKey: 'status', title: '状态', width: 110 },
  { colKey: 'ban_reason', title: '封禁原因', width: 180 },
  { colKey: 'created_at', title: '首次出现', width: 170 },
  { colKey: 'ops', title: '操作', width: 130, fixed: 'right' },
]

const auth = useAuthStore()
const items = ref<UserView[]>([])
const total = ref(0)
const loading = ref(false)
const keyword = ref('')
const page = ref(1)
const pageSize = 20

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

// 封禁弹窗
const banVisible = ref(false)
const banForm = reactive({
  id: 0,
  nickname: '',
  platform: '',
  platform_user_id: '',
  reason: '',
})

async function load() {
  loading.value = true
  try {
    const res = await contentApi.users(keyword.value, page.value, pageSize)
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

function search() {
  page.value = 1
  void load()
}

function openBan(row: UserView) {
  banForm.id = row.id
  banForm.nickname = row.nickname || row.platform_user_id
  banForm.platform = row.platform || ''
  banForm.platform_user_id = row.platform_user_id || ''
  banForm.reason = ''
  banVisible.value = true
}

function confirmBan() {
  if (!banForm.reason.trim()) {
    MessagePlugin.warning('请填写封禁原因')
    return
  }
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await contentApi.setUserBan(banForm.id, true, banForm.reason.trim(), token)
    MessagePlugin.success('已封禁该用户')
    banVisible.value = false
    await load()
  }
}

function confirmUnban(row: UserView) {
  const dialog = DialogPlugin.confirm({
    header: '解封用户',
    body: `确定解除「${row.nickname || row.platform_user_id}」的封禁吗？`,
    theme: 'warning',
    confirmBtn: '解封',
    cancelBtn: '取消',
    onConfirm: async () => {
      stepUpVisible.value = true
      pendingAction.value = async (token) => {
        await contentApi.setUserBan(row.id, false, '', token)
        MessagePlugin.success('已解除封禁')
        dialog.destroy()
        await load()
      }
    },
  })
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

function formatTime(s: string): string {
  return new Date(s).toLocaleString()
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">用户</h2>
      <div class="header-search">
        <t-input
          v-model="keyword"
          placeholder="搜索昵称 / 平台用户 ID…"
          clearable
          :style="{ width: 240 }"
          @enter="search"
          @clear="search"
        />
        <t-button theme="primary" :loading="loading" @click="search">查询</t-button>
      </div>
    </div>

    <t-card>
      <t-table
        :data="items"
        :loading="loading"
        row-key="id"
        :pagination="{ current: page, pageSize, total, onChange: pageChange }"
        :columns="columns"
      >
        <template #id="{ row }">{{ row.id }}</template>
        <template #nickname="{ row }">
          <div class="user-cell">
            <t-avatar size="small">{{ (row.nickname || '?').slice(0, 1) }}</t-avatar>
            <span>{{ row.nickname || '—' }}</span>
          </div>
        </template>
        <template #platform="{ row }">
          <t-tag theme="primary" variant="light">{{ row.platform }}</t-tag>
        </template>
        <template #platform_user_id="{ row }">{{ row.platform_user_id }}</template>
        <template #status="{ row }">
          <t-tag v-if="row.banned_at" theme="danger" variant="light">已封禁</t-tag>
          <t-tag v-else theme="success" variant="light">正常</t-tag>
        </template>
        <template #ban_reason="{ row }">
          <span v-if="row.banned_at" class="ban-reason">{{ row.ban_reason || '—' }}</span>
          <span v-else class="text-muted">—</span>
        </template>
        <template #created_at="{ row }">{{ formatTime(row.created_at) }}</template>
        <template #ops="{ row }">
          <t-button v-if="auth.isSuper" v-show="!row.banned_at" size="small" variant="base" theme="danger" @click="openBan(row)">
            封禁
          </t-button>
          <t-button v-if="auth.isSuper" v-show="row.banned_at" size="small" variant="base" @click="confirmUnban(row)">解封</t-button>
        </template>
      </t-table>
    </t-card>

    <!-- 封禁弹窗 -->
    <t-dialog v-model:visible="banVisible" :close-on-overlay-click="false" :footer="false" width="420px">
      <template #header>
        <div class="ban-dialog-title">封禁用户</div>
      </template>
      <p class="ban-target">
        目标：<b>{{ banForm.nickname }}</b>
        <span v-if="banForm.platform" class="ban-target-meta">{{ banForm.platform }} · {{ banForm.platform_user_id }}</span>
        <span v-else class="ban-target-meta">{{ banForm.platform_user_id }}</span>
        （封禁后其消息将被静默忽略）
      </p>
      <t-textarea
        v-model="banForm.reason"
        :autosize="{ minRows: 2, maxRows: 4 }"
        placeholder="请填写封禁原因（将展示在用户列表中）"
      />
      <div class="ban-actions">
        <t-button variant="outline" @click="banVisible = false">取消</t-button>
        <t-button theme="danger" @click="confirmBan">确认封禁</t-button>
      </div>
    </t-dialog>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.user-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ban-reason {
  color: var(--mgr-danger);
}
.text-muted {
  color: var(--mgr-text-muted);
}
.ban-target {
  margin: 0 0 12px;
  color: var(--mgr-text-secondary);
}
.ban-target-meta {
  margin-left: 6px;
  font-family: 'Cascadia Code', Consolas, monospace;
  font-size: 12px;
  color: var(--mgr-text-muted);
}
.ban-dialog-title {
  font-size: 18px;
  font-weight: 600;
}
/* 原因输入框与按钮之间留出垂直间距；取消/确认按钮间距收紧 */
.ban-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 16px;
}
</style>
