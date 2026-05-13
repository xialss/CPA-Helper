<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { NButton, NForm, NFormItem, NInput, NSpace, useMessage } from 'naive-ui'
import { ShieldCheck, UserRound } from 'lucide-vue-next'

import { changeCredentials, getMe } from '@/features/auth/api/authApi'
import { setCurrentUser } from '@/features/auth/state/currentUser'

const message = useMessage()
const isLoading = ref(false)
const isSaving = ref(false)
const isAdmin = ref(false)

const accountForm = reactive({
  username: '',
  password: '',
  current_password: '',
})

async function refresh() {
  isLoading.value = true
  try {
    const user = await getMe()
    setCurrentUser(user)
    accountForm.username = user.username
    isAdmin.value = user.is_admin
  } catch (error) {
    message.error(error instanceof Error ? error.message : '加载账户失败')
  } finally {
    isLoading.value = false
  }
}

async function saveAccount() {
  isSaving.value = true
  try {
    const user = await changeCredentials({
      password: accountForm.password,
      current_password: accountForm.current_password || undefined,
    })
    setCurrentUser(user)
    window.dispatchEvent(new CustomEvent('cpa:account-updated', { detail: user }))
    accountForm.username = user.username
    accountForm.password = ''
    accountForm.current_password = ''
    message.success('账户已更新')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '账户更新失败')
  } finally {
    isSaving.value = false
  }
}

onMounted(refresh)
</script>

<template>
  <section class="page">
    <div class="page-header">
      <div>
        <h1 class="page-title">账户设置</h1>
        <p class="page-subtitle">查看账号并更新当前登录密码</p>
      </div>
      <NSpace>
        <NButton secondary :loading="isLoading" @click="refresh">刷新</NButton>
        <NButton type="primary" :loading="isSaving" @click="saveAccount">保存账户</NButton>
      </NSpace>
    </div>

    <div class="metric-grid account-settings-metrics">
      <div class="metric-card is-teal">
        <div class="metric-icon" aria-hidden="true">
          <UserRound :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">当前账号</div>
        <div class="metric-value">{{ accountForm.username || '-' }}</div>
        <div class="metric-footnote">登录身份</div>
      </div>
      <div class="metric-card is-purple">
        <div class="metric-icon" aria-hidden="true">
          <ShieldCheck :size="20" :stroke-width="2.2" />
        </div>
        <div class="metric-label">权限</div>
        <div class="metric-value">{{ isAdmin ? '管理员' : '普通账户' }}</div>
        <div class="metric-footnote">当前会话</div>
      </div>
    </div>

    <section class="panel">
      <div class="panel-inner">
        <h2 class="section-title">账号与密码</h2>
        <NForm :model="accountForm" label-placement="top">
          <div class="form-grid">
            <NFormItem label="账号">
              <NInput v-model:value="accountForm.username" autocomplete="username" disabled />
            </NFormItem>
            <NFormItem label="当前密码">
              <NInput
                v-model:value="accountForm.current_password"
                type="password"
                show-password-on="mousedown"
                autocomplete="current-password"
              />
            </NFormItem>
            <NFormItem label="新密码">
              <NInput
                v-model:value="accountForm.password"
                type="password"
                show-password-on="mousedown"
                autocomplete="new-password"
                @keyup.enter="saveAccount"
              />
            </NFormItem>
          </div>
        </NForm>
      </div>
    </section>
  </section>
</template>

<style scoped>
.account-settings-metrics {
  grid-template-columns: repeat(2, minmax(180px, 1fr));
}

.form-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 12px;
}

@media (max-width: 720px) {
  .account-settings-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (max-width: 900px) {
  .form-grid {
    grid-template-columns: 1fr;
  }
}
</style>
