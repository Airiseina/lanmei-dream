<script setup lang="ts">
// 管理员管理：列表 / 创建 / 编辑 / 启停 / 重置密码 / 删除（写操作均需 super + step-up）
import { onMounted, reactive, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol } from 'tdesign-vue-next'
import { KeyRound, Plus, Trash2, UserCog } from 'lucide-vue-next'
import { adminApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { Admin } from '@/types/api'

const auth = useAuthStore()

// 列定义（TDesign Vue Next 需用 columns prop，子组件 t-table-column 不受支持）
const columns: PrimaryTableCol[] = [
  { colKey: 'id', title: 'ID', width: 60, align: 'center' },
  { colKey: 'username', title: '用户名' },
  { colKey: 'role', title: '角色', width: 110 },
  { colKey: 'status', title: '状态', width: 90 },
  { colKey: 'last_login_at', title: '最后登录', width: 180 },
  { colKey: 'created_at', title: '创建时间', width: 180 },
  { colKey: 'ops', title: '操作', width: 220, fixed: 'right' },
]

const items = ref<Admin[]>([])
const total = ref(0)
const loading = ref(false)
const page = ref(1)
const pageSize = 20

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

// 创建/编辑对话框
const editVisible = ref(false)
const editing = ref<Admin | null>(null)
const form = reactive({
  username: '',
  password: '',
  role: 'admin',
  display_name: '',
})

async function load() {
  loading.value = true
  try {
    const res = await adminApi.list(page.value, pageSize)
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

function openCreate() {
  editing.value = null
  Object.assign(form, { username: '', password: '', role: 'admin', display_name: '' })
  editVisible.value = true
}

function openEdit(admin: Admin) {
  editing.value = admin
  Object.assign(form, {
    username: admin.username,
    password: '',
    role: admin.role,
    display_name: admin.display_name,
  })
  editVisible.value = true
}

async function save() {
  if (!form.username) {
    MessagePlugin.warning('用户名不能为空')
    return
  }
  if (!editing.value && form.password.length < 8) {
    MessagePlugin.warning('密码至少 8 位')
    return
  }
  // 写操作需 step-up
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    if (editing.value) {
      await adminApi.update(editing.value.id, {
        display_name: form.display_name,
        role: form.role,
        ...(form.password ? { password: form.password } : {}),
      }, token)
      MessagePlugin.success('已更新')
    } else {
      await adminApi.create(
        { username: form.username, password: form.password, role: form.role, display_name: form.display_name },
        token,
      )
      MessagePlugin.success('已创建')
    }
    editVisible.value = false
    await load()
  }
}

async function toggleStatus(admin: Admin) {
  const target = admin.status === 'active' ? 'disabled' : 'active'
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await adminApi.setStatus(admin.id, target, token)
    MessagePlugin.success(target === 'active' ? '已启用' : '已禁用')
    await load()
  }
}

async function resetPassword(admin: Admin) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    const pwd = prompt(`为 ${admin.username} 设置新密码（至少 8 位）：`)
    if (!pwd) return
    if (pwd.length < 8) {
      MessagePlugin.warning('密码至少 8 位')
      return
    }
    await adminApi.resetPassword(admin.id, pwd, token)
    MessagePlugin.success('密码已重置，该账号需重新登录')
    await load()
  }
}

async function remove(admin: Admin) {
  const confirm = DialogPlugin.confirm({
    header: '删除管理员',
    body: `确定删除管理员「${admin.username}」吗？此操作不可恢复。`,
    theme: 'danger',
    confirmBtn: { content: '删除', theme: 'danger' },
    onConfirm: async () => {
      stepUpVisible.value = true
      pendingAction.value = async (token) => {
        await adminApi.delete(admin.id, token)
        MessagePlugin.success('已删除')
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

function formatTime(s?: string | null): string {
  return s ? new Date(s).toLocaleString() : '—'
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">管理员</h2>
      <t-button v-if="auth.isSuper" theme="primary" @click="openCreate">
        <template #icon><Plus :size="16" /></template>
        新建管理员
      </t-button>
    </div>

    <t-card>
      <t-table
        :data="items"
        :columns="columns"
        :loading="loading"
        row-key="id"
        :pagination="{
          current: page,
          pageSize,
          total,
          onChange: pageChange,
        }"
      >
        <template #id="{ row }">{{ row.id }}</template>
        <template #username="{ row }">
          <div class="user-cell">
            <t-avatar size="small">{{ (row.display_name || row.username).slice(0, 1) }}</t-avatar>
            <div>
              <div>{{ row.display_name || row.username }}</div>
              <div class="cell-sub">{{ row.username }}</div>
            </div>
          </div>
        </template>
        <template #role="{ row }">
          <t-tag :theme="row.role === 'super_admin' ? 'danger' : 'primary'" variant="light">
            {{ row.role === 'super_admin' ? '超级管理员' : '普通管理员' }}
          </t-tag>
        </template>
        <template #status="{ row }">
          <t-tag :theme="row.status === 'active' ? 'success' : 'default'" variant="light">
            {{ row.status === 'active' ? '正常' : '禁用' }}
          </t-tag>
        </template>
        <template #last_login_at="{ row }">{{ formatTime(row.last_login_at) }}</template>
        <template #created_at="{ row }">{{ formatTime(row.created_at) }}</template>
        <template #ops="{ row }">
          <t-space v-if="auth.isSuper && row.id !== auth.me?.id" size="4">
            <t-button variant="base" size="small" @click="openEdit(row)"><template #icon><UserCog :size="15" /></template>编辑</t-button>
            <t-button variant="base" size="small" @click="toggleStatus(row)">{{ row.status === 'active' ? '禁用' : '启用' }}</t-button>
            <t-button variant="base" size="small" @click="resetPassword(row)"><template #icon><KeyRound :size="15" /></template>重置密码</t-button>
            <t-button variant="base" size="small" theme="danger" @click="remove(row)"><template #icon><Trash2 :size="15" /></template>删除</t-button>
          </t-space>
          <t-button v-else-if="auth.me?.id === row.id" variant="base" size="small" @click="openEdit(row)">编辑</t-button>
        </template>
      </t-table>
    </t-card>

    <!-- 创建 / 编辑 -->
    <t-dialog
      v-model:visible="editVisible"
      :header="editing ? '编辑管理员' : '新建管理员'"
      :confirm-btn="{ content: '保存', theme: 'primary' }"
      cancel-btn="取消"
      @confirm="save"
    >
      <t-form label-align="top">
        <t-form-item label="用户名">
          <t-input v-model="form.username" :disabled="Boolean(editing)" placeholder="登录用户名" />
        </t-form-item>
        <t-form-item label="显示名称">
          <t-input v-model="form.display_name" placeholder="展示名称（可留空）" />
        </t-form-item>
        <t-form-item v-if="!editing" label="角色">
          <t-radio-group v-model="form.role">
            <t-radio-button value="admin">普通管理员</t-radio-button>
            <t-radio-button value="super_admin">超级管理员</t-radio-button>
          </t-radio-group>
        </t-form-item>
        <t-form-item :label="editing ? '重置密码（留空不修改）' : '初始密码'">
          <t-input v-model="form.password" type="password" placeholder="至少 8 位" autocomplete="new-password" />
        </t-form-item>
      </t-form>
    </t-dialog>

    <!-- step-up 二次验证 -->
    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.user-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.cell-sub {
  font-size: 12px;
  color: var(--mgr-text-secondary);
}
</style>
