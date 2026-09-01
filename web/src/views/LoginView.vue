<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { NForm, NFormItem, NInput, NButton, NAlert, useMessage } from 'naive-ui'
import { api } from '@/api/client'

const router = useRouter()
const message = useMessage()
const password = ref('')
const loading = ref(false)
const error = ref('')

async function submit() {
  if (!password.value) {
    error.value = '请输入密码'
    return
  }
  loading.value = true
  error.value = ''
  try {
    await api.login(password.value)
    router.push('/')
  } catch (e) {
    error.value = '密码错误或已被锁定,请稍后再试'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="brand">
        <svg viewBox="0 0 64 64" class="brand-logo">
          <rect width="64" height="64" rx="12" fill="currentColor" opacity="0.12" />
          <path d="M14 20 L28 32 L14 44" fill="none" stroke="currentColor" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" />
          <path d="M30 46 L50 46" stroke="currentColor" stroke-width="5" stroke-linecap="round" />
        </svg>
        <h1>LightRemote</h1>
        <p>超轻量远程工作台</p>
      </div>

      <n-form @submit.prevent="submit">
        <n-form-item label="密码">
          <n-input
            v-model:value="password"
            type="password"
            placeholder="请输入登录密码"
            show-password-on="click"
            :input-props="{ autocomplete: 'current-password' }"
            @keydown.enter="submit"
          />
        </n-form-item>
        <n-alert v-if="error" type="error" :show-icon="false" style="margin-bottom: 12px">
          {{ error }}
        </n-alert>
        <n-button type="primary" block :loading="loading" @click="submit">登录</n-button>
      </n-form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
  background: var(--lr-bg);
}
.login-card {
  width: 100%;
  max-width: 360px;
  background: var(--lr-bg-elevated);
  border-radius: var(--lr-radius);
  padding: 28px 24px;
  box-shadow: 0 4px 24px rgba(0, 0, 0, 0.08);
}
.brand { text-align: center; margin-bottom: 24px; }
.brand-logo { width: 48px; height: 48px; }
.brand h1 { font-size: 22px; margin: 8px 0 4px; }
.brand p { color: var(--lr-fg-muted); margin: 0; font-size: 13px; }
</style>
