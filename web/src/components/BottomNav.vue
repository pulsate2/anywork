<script setup lang="ts">
import { useRoute } from 'vue-router'
import { NIcon } from 'naive-ui'
import { TerminalOutline, FolderOpenOutline, FolderOutline, GitBranchOutline, SettingsOutline, SparklesOutline } from '@vicons/ionicons5'

const route = useRoute()

const items = [
  { name: 'home', label: '工作区', path: '/', icon: FolderOpenOutline },
  { name: 'terminal', label: '终端', path: '/terminal', icon: TerminalOutline },
  { name: 'files', label: '文件', path: '/files', icon: FolderOutline },
  { name: 'git', label: 'Git', path: '/git', icon: GitBranchOutline },
  { name: 'ai', label: 'AI 配置', path: '/ai', icon: SparklesOutline },
  { name: 'settings', label: '设置', path: '/settings', icon: SettingsOutline },
]
</script>

<template>
  <!-- 登录页没有可切换的页面,导航只会挡住登录框 -->
  <nav v-if="route.name !== 'login'" class="bottom-nav">
    <router-link
      v-for="it in items"
      :key="it.name"
      :to="it.path"
      class="nav-item"
      :class="{ active: route.name === it.name }"
    >
      <n-icon :component="it.icon" />
      <span>{{ it.label }}</span>
    </router-link>
  </nav>
</template>
<style scoped>
</style>
