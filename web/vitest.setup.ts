// 视图测试大多只挂载单个组件,不装真实 router。但组件 setup 里会调用
// useRoute()/useRouter()(vue-router 4 直接从 inject 取,没装就返回 undefined,
// 之后访问 route.query 直接抛——AgentView 的 watch 取初值就会走到那里)。
// 这里给所有测试挂一份内存路由桩:路由表只求让 push/replace 能解析,query 起始为空。
// 需要断言路由行为的测试(html-preview)自带 vi.mock('vue-router') 覆盖,互不影响。
import { config } from '@vue/test-utils'
import { createRouter, createMemoryHistory } from 'vue-router'

const router = createRouter({
  history: createMemoryHistory(),
  routes: [
    { path: '/', name: 'home', component: { template: '<div />' } },
    { path: '/agent', name: 'agent', component: { template: '<div />' } },
    { path: '/files/file', name: 'files-file', component: { template: '<div />' } },
  ],
})

config.global.plugins = [router]
