<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NCard, NForm, NFormItem, NInput, useMessage } from 'naive-ui'

import { changeCredentials, getMe } from '@/features/auth/api/authApi'
import { setCurrentUser } from '@/features/auth/state/currentUser'
import { logoUrl } from '@/shared/utils/assets'

const router = useRouter()
const message = useMessage()
const isLoading = ref(false)
const errorMessage = ref<string | null>(null)
const form = reactive({
  username: '',
  password: '',
  current_password: '',
})

onMounted(async () => {
  try {
    const user = await getMe()
    form.username = user.username
  } catch {
    form.username = ''
  }
})

async function handleSubmit() {
  isLoading.value = true
  errorMessage.value = null
  try {
    const user = await changeCredentials({
      password: form.password,
      current_password: form.current_password || undefined,
    })
    setCurrentUser(user)
    message.success('密码已更新')
    await router.push(user.is_admin ? '/admin/usage' : '/account/usage')
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '更新失败'
  } finally {
    isLoading.value = false
  }
}
</script>

<template>
  <main class="auth-screen">
    <section class="auth-brand-panel" aria-hidden="true">
      <div class="brand-stage">
        <span class="brand-word brand-word-cpa">CPA</span>
        <span class="brand-word brand-word-helper">HELPER</span>
      </div>
    </section>

    <section class="auth-content" aria-label="修改密码区域">
      <div class="brand-mark">
        <img :src="logoUrl" alt="">
      </div>

      <NCard class="auth-card" :bordered="true">
        <div class="auth-heading">
          <h1>修改密码</h1>
          <p>首次登录后需要完成密码更新</p>
        </div>

        <NAlert v-if="errorMessage" type="error" :bordered="false" class="auth-alert">
          {{ errorMessage }}
        </NAlert>

        <NForm :model="form" label-placement="top" @submit.prevent="handleSubmit">
          <NFormItem label="账号" path="username">
            <NInput v-model:value="form.username" autocomplete="username" disabled />
          </NFormItem>
          <NFormItem label="新密码" path="password">
            <NInput
              v-model:value="form.password"
              type="password"
              show-password-on="mousedown"
              autocomplete="new-password"
            />
          </NFormItem>
          <NFormItem label="当前密码" path="current_password">
            <NInput
              v-model:value="form.current_password"
              type="password"
              show-password-on="mousedown"
              autocomplete="current-password"
              @keyup.enter="handleSubmit"
            />
          </NFormItem>
          <NButton type="primary" block attr-type="submit" :loading="isLoading">
            保存
          </NButton>
        </NForm>
      </NCard>
    </section>
  </main>
</template>

<style scoped>
.auth-screen {
  display: grid;
  grid-template-columns: minmax(420px, 1fr) minmax(420px, 1fr);
  height: 100vh;
  height: 100dvh;
  min-height: 0;
  overflow: auto;
  background: var(--cpa-bg);
}

.auth-brand-panel {
  position: relative;
  display: grid;
  min-height: 100%;
  align-items: center;
  overflow: hidden;
  padding: 72px 48px;
  background: #030303;
}

.brand-stage {
  position: relative;
  display: grid;
  width: 100%;
  gap: 18px;
  align-content: center;
  font-weight: 850;
  line-height: 0.86;
  text-transform: uppercase;
}

.brand-word {
  display: block;
  max-width: 100%;
  overflow-wrap: anywhere;
  letter-spacing: 0;
}

.brand-word-cpa {
  justify-self: start;
  color: #d6d6d6;
  font-size: 172px;
}

.brand-word-helper {
  justify-self: end;
  color: #8d8d8d;
  font-size: 136px;
}

.auth-content {
  display: grid;
  min-width: 0;
  align-content: center;
  justify-items: center;
  gap: 28px;
  padding: 48px;
  background:
    linear-gradient(135deg, var(--cpa-bg-glow) 0, transparent 30%),
    linear-gradient(180deg, var(--cpa-bg-soft) 0, var(--cpa-bg) 560px),
    var(--cpa-bg);
}

.auth-card {
  width: min(420px, 100%);
  border: 1px solid var(--cpa-border);
  border-radius: var(--cpa-radius);
  overflow: hidden;
  background: var(--cpa-surface-raised);
  box-shadow: 0 22px 54px rgb(24 45 53 / 10%), var(--cpa-shadow-hairline);
}

.auth-card :deep(.n-card__content) {
  padding: 36px 32px 32px;
}

.auth-heading {
  display: grid;
  justify-items: center;
  margin-bottom: 24px;
  text-align: center;
}

.brand-mark {
  display: grid;
  width: 76px;
  height: 76px;
  place-items: center;
  border-radius: 18px;
  overflow: hidden;
  background: var(--cpa-surface-solid);
  box-shadow: 0 18px 34px rgb(0 154 168 / 18%);
}

.brand-mark img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
}

h1 {
  margin: 0;
  color: var(--cpa-text-strong);
  font-size: 24px;
  font-weight: 800;
  line-height: 1.18;
  text-wrap: pretty;
}

p {
  margin: 6px 0 0;
  color: var(--cpa-text-muted);
  text-wrap: pretty;
}

.auth-alert {
  margin-bottom: 12px;
}

.auth-card :deep(.n-form-item-label) {
  font-weight: 650;
}

:global(:root.dark) .auth-brand-panel {
  background: #020202;
}

@media (max-width: 1320px) {
  .brand-word-cpa {
    font-size: 148px;
  }

  .brand-word-helper {
    font-size: 118px;
  }
}

@media (max-width: 1180px) {
  .auth-screen {
    grid-template-columns: minmax(360px, 0.9fr) minmax(390px, 1.1fr);
  }

  .brand-word-cpa {
    font-size: 118px;
  }

  .brand-word-helper {
    font-size: 94px;
  }
}

@media (max-width: 900px) {
  .auth-screen {
    grid-template-columns: 1fr;
  }

  .auth-brand-panel {
    display: none;
  }

  .auth-content {
    min-height: 100%;
    align-content: start;
    gap: 16px;
    padding: max(28px, env(safe-area-inset-top)) 14px 20px;
  }

  .brand-mark {
    width: 56px;
    height: 56px;
    border-radius: 15px;
  }

  .auth-card {
    align-self: center;
  }
}

@media (max-width: 520px) {
  .auth-content {
    gap: 14px;
  }

  .auth-card :deep(.n-card__content) {
    padding: 24px 18px 22px;
  }

  h1 {
    font-size: 22px;
  }
}
</style>
