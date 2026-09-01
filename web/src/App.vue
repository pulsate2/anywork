<script setup lang="ts">
import { ref } from 'vue'
import { NConfigProvider, NMessageProvider, NDialogProvider, zhCN, dateZhCN, darkTheme, type GlobalThemeOverrides } from 'naive-ui'
import BottomNav from '@/components/BottomNav.vue'

// 跟随系统深浅主题(后续做成设置项)。
const systemDark = window.matchMedia('(prefers-color-scheme: dark)').matches
const isDark = ref(systemDark)

// 触控友好 + 品牌配色覆盖。
const themeOverrides: GlobalThemeOverrides = {
  common: {
    borderRadius: '10px',
    primaryColor: '#2563eb',
    primaryColorHover: '#3b82f6',
    primaryColorPressed: '#1d4ed8',
  },
  Button: {
    heightMedium: '44px',
    heightLarge: '48px',
  },
  Input: {
    heightMedium: '44px',
  },
}
</script>

<template>
  <n-config-provider
    :theme="isDark ? darkTheme : undefined"
    :theme-overrides="themeOverrides"
    :locale="zhCN"
    :date-locale="dateZhCN"
  >
    <n-dialog-provider>
      <n-message-provider placement="top">
        <div class="app-root">
          <router-view />
          <BottomNav />
        </div>
      </n-message-provider>
    </n-dialog-provider>
  </n-config-provider>
</template>

<style scoped>
.app-root { min-height: 100%; }
</style>
