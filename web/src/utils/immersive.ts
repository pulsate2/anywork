// 沉浸模式标记:全屏子视图(如 Agent 聊天)打开时收掉底部导航。
// 声明成模块级单例 —— 状态在 AgentView 里,消费方在 BottomNav,跨组件只能靠它。
import { ref } from 'vue'

const immersive = ref(false)

export function useImmersive() {
  return immersive
}
