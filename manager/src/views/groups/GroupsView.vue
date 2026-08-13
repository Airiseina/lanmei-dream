<script setup lang="ts">
// 群组管理：跨链路表聚合的群列表 + 群配置编辑（写操作需 super + step-up）
import { onMounted, reactive, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol } from 'tdesign-vue-next'
import { Settings2 } from 'lucide-vue-next'
import { contentApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { GroupView } from '@/types/api'

const columns: PrimaryTableCol[] = [
  { colKey: 'group_id', title: '群 ID' },
  { colKey: 'bot_enabled', title: 'Bot 开关', width: 110 },
  { colKey: 'remark', title: '标记', width: 160 },
  { colKey: 'welcome_msg', title: '欢迎语', width: 180 },
  { colKey: 'has_config', title: '配置', width: 90 },
  { colKey: 'ops', title: '操作', width: 180, fixed: 'right' },
]

const auth = useAuthStore()

const items = ref<GroupView[]>([])
const total = ref(0)
const loading = ref(false)
const keyword = ref('')
const page = ref(1)
const pageSize = 20

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

// 配置编辑抽屉
const drawerVisible = ref(false)
const editing = ref<GroupView | null>(null)
const form = reactive({
  bot_enabled: true as boolean | null,
  topic_enabled: true as boolean | null,
  credit_enabled: true as boolean | null,
  whitelist: '',
  blacklist: '',
  welcome_msg: '',
  remark: '',
})

async function load() {
  loading.value = true
  try {
    const res = await contentApi.groups(keyword.value, page.value, pageSize)
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

async function openEdit(g: GroupView) {
  try {
    const cfg = await contentApi.groupConfig(g.platform, g.group_id)
    editing.value = cfg
    Object.assign(form, {
      bot_enabled: cfg.bot_enabled ?? null,
      topic_enabled: cfg.topic_enabled ?? null,
      credit_enabled: cfg.credit_enabled ?? null,
      whitelist: (cfg.whitelist ?? []).join('\n'),
      blacklist: (cfg.blacklist ?? []).join('\n'),
      welcome_msg: cfg.welcome_msg ?? '',
      remark: cfg.remark ?? '',
    })
    drawerVisible.value = true
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '配置加载失败')
  }
}

async function save() {
  if (!editing.value) return
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    const whitelist = form.whitelist.split('\n').map((s) => s.trim()).filter(Boolean)
    const blacklist = form.blacklist.split('\n').map((s) => s.trim()).filter(Boolean)
    await contentApi.saveGroupConfig(
      editing.value!.platform,
      editing.value!.group_id,
      {
        bot_enabled: form.bot_enabled,
        topic_enabled: form.topic_enabled,
        credit_enabled: form.credit_enabled,
        whitelist,
        blacklist,
        welcome_msg: form.welcome_msg,
        remark: form.remark,
      },
      token,
    )
    MessagePlugin.success('群配置已保存')
    drawerVisible.value = false
    await load()
  }
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

// 快速开关：读取当前配置后仅翻转 bot_enabled（其余字段原样回写）
async function toggleBot(g: GroupView) {
  const target = g.bot_enabled === false
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    const cfg = await contentApi.groupConfig(g.platform, g.group_id)
    await contentApi.saveGroupConfig(
      g.platform,
      g.group_id,
      {
        bot_enabled: target,
        whitelist: cfg.whitelist ?? [],
        blacklist: cfg.blacklist ?? [],
        welcome_msg: cfg.welcome_msg ?? '',
        remark: cfg.remark ?? '',
      },
      token,
    )
    MessagePlugin.success(target ? '已启用该群 Bot' : '已禁用该群 Bot（群内静默）')
    await load()
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">群组</h2>
      <div class="header-search">
        <t-input
          v-model="keyword"
          placeholder="搜索群 ID…"
          clearable
          :style="{ width: 220 }"
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
        row-key="group_id"
        :pagination="{ current: page, pageSize, total, onChange: pageChange }"
        :columns="columns"
      >
        <template #group_id="{ row }">
          <div class="group-cell">
            <span class="group-id">{{ row.group_id }}</span>
            <t-tag v-if="row.platform" theme="primary" variant="light" size="small">{{ row.platform }}</t-tag>
          </div>
        </template>
        <template #bot_enabled="{ row }">
          <t-tag v-if="row.bot_enabled === true" theme="success" variant="light">已开启</t-tag>
          <t-tag v-else-if="row.bot_enabled === false" theme="danger" variant="light">已关闭</t-tag>
          <t-tag v-else theme="default" variant="light">默认</t-tag>
        </template>
        <template #welcome_msg="{ row }">{{ row.welcome_msg || '—' }}</template>
        <template #remark="{ row }">
          <span v-if="row.remark" class="remark">{{ row.remark }}</span>
          <span v-else class="text-muted">—</span>
        </template>
        <template #has_config="{ row }">
          <t-tag :theme="row.has_config ? 'primary' : 'default'" variant="light">
            {{ row.has_config ? '已配置' : '默认' }}
          </t-tag>
        </template>
        <template #ops="{ row }">
          <t-button v-if="auth.isSuper" size="small" variant="base" :theme="row.bot_enabled === false ? 'primary' : 'default'" @click="toggleBot(row)">
            {{ row.bot_enabled === false ? '启用' : '禁用' }}
          </t-button>
          <t-button v-if="auth.isSuper" size="small" variant="base" @click="openEdit(row)">
            <template #icon><Settings2 :size="14" /></template>配置
          </t-button>
        </template>
      </t-table>
    </t-card>

    <!-- 群配置抽屉 -->
    <t-drawer v-model:visible="drawerVisible" :header="`群配置：${editing?.group_id ?? ''}`" size="420px" :footer="false">
      <t-form label-align="top">
        <t-form-item label="Bot 开关">
          <t-radio-group v-model="form.bot_enabled">
            <t-radio-button :value="true">开启</t-radio-button>
            <t-radio-button :value="false">关闭</t-radio-button>
            <t-radio-button :value="null">默认</t-radio-button>
          </t-radio-group>
        </t-form-item>
        <t-form-item label="话题模式">
          <t-radio-group v-model="form.topic_enabled">
            <t-radio-button :value="true">开启</t-radio-button>
            <t-radio-button :value="false">关闭</t-radio-button>
            <t-radio-button :value="null">默认</t-radio-button>
          </t-radio-group>
        </t-form-item>
        <t-form-item label="积分功能">
          <t-radio-group v-model="form.credit_enabled">
            <t-radio-button :value="true">开启</t-radio-button>
            <t-radio-button :value="false">关闭</t-radio-button>
            <t-radio-button :value="null">默认</t-radio-button>
          </t-radio-group>
        </t-form-item>
        <t-form-item label="白名单（每行一个用户 ID）">
          <t-textarea v-model="form.whitelist" :autosize="{ minRows: 3, maxRows: 6 }" placeholder="platform:userID" />
        </t-form-item>
        <t-form-item label="黑名单（每行一个用户 ID）">
          <t-textarea v-model="form.blacklist" :autosize="{ minRows: 3, maxRows: 6 }" placeholder="platform:userID" />
        </t-form-item>
        <t-form-item label="欢迎语">
          <t-textarea v-model="form.welcome_msg" :autosize="{ minRows: 2, maxRows: 4 }" placeholder="群欢迎消息（留空不发送）" />
        </t-form-item>
        <t-form-item label="标记（管理备注，仅面板可见）">
          <t-input v-model="form.remark" placeholder="例如：重点群 / 需观察…" />
        </t-form-item>
      </t-form>
      <t-space class="drawer-actions">
        <t-button theme="primary" @click="save">保存</t-button>
        <t-button variant="outline" @click="drawerVisible = false">取消</t-button>
      </t-space>
    </t-drawer>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.group-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.group-id {
  font-weight: 500;
}
.remark {
  color: var(--mgr-warning);
}
.text-muted {
  color: var(--mgr-text-muted);
}
/* 群配置抽屉：按钮组与上方表单之间留出间隔 */
.drawer-actions {
  margin-top: 24px;
}
</style>
