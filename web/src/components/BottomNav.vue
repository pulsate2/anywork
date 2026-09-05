<script setup lang="ts">
import { useRoute } from 'vue-router'
import { NIcon } from 'naive-ui'
import { TerminalOutline, FolderOpenOutline, FolderOutline, GitBranchOutline, SettingsOutline, SparklesOutline } from '@vicons/ionicons5'
import { useSoftKeyboard } from '@/utils/softKeyboard'

const route = useRoute()

// 软键盘弹起时把这条导航收掉(样式见 main.css 的 .bottom-nav.kb-open)。
// 它是 position: fixed + bottom: 0,贴的是 layout viewport 的下沿 —— 而软键盘要么只压缩
// visual viewport 并把它往下挪(Chrome),要么把整个 WebView 变矮(Via 那类),两种情况下
// 这条导航都会贴着键盘上沿露出来,挡住正在输入的那一行,看着就像「拉起键盘把导航也拉起来了」。
// 它其实没动,是视口挪到了它跟前。键盘开着的时候没人要切页,收掉最省事。
const { open: kbOpen } = useSoftKeyboard()

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
  <nav v-if="route.name !== 'login'" class="bottom-nav" :class="{ 'kb-open': kbOpen }">
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
