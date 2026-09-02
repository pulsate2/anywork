// AI 供应商预设。跟 cc-switch 一样只放在前端:预设只是"填表模板",选完就变成
// 一份普通配置存进库里,以后改预设表也不会动到用户已经保存的配置。
import type { AiApp } from '@/api/client'

// 预设里的 API Key 位置用占位符占着,保存时才换成用户输入的真值。
export const API_KEY_PLACEHOLDER = '{{API_KEY}}'

export interface AiPreset {
  app: AiApp
  name: string
  category: string
  websiteUrl: string
  config: string // claude: settings.json 文本;codex: config.toml 文本
  auth?: string // codex 专用:auth.json 文本
}

export const CATEGORY_LABELS: Record<string, string> = {
  official: '官方',
  cn_official: '国产官方',
  aggregator: '聚合',
  third_party: '第三方',
  custom: '自定义',
}

export function categoryLabel(c: string): string {
  return CATEGORY_LABELS[c] || c
}

interface ClaudeRow {
  name: string
  category: string
  site: string
  baseUrl: string // 留空 = 官方直连,settings.json 里不写任何 env
  model?: string // 填了就连 haiku/sonnet/opus 三个别名一起钉住
  keyField?: string // 少数供应商只认 ANTHROPIC_API_KEY,不吃 AUTH_TOKEN
  extra?: Record<string, string | number>
}

interface CodexRow {
  name: string
  category: string
  site: string
  provider: string // [model_providers.custom] 里的 name
  baseUrl: string
  model: string
}

function claudeConfig(r: ClaudeRow): string {
  if (!r.baseUrl) return JSON.stringify({ env: {} }, null, 2)
  const env: Record<string, string | number> = { ANTHROPIC_BASE_URL: r.baseUrl }
  env[r.keyField || 'ANTHROPIC_AUTH_TOKEN'] = API_KEY_PLACEHOLDER
  if (r.model) {
    env.ANTHROPIC_MODEL = r.model
    env.ANTHROPIC_DEFAULT_HAIKU_MODEL = r.model
    env.ANTHROPIC_DEFAULT_SONNET_MODEL = r.model
    env.ANTHROPIC_DEFAULT_OPUS_MODEL = r.model
  }
  return JSON.stringify({ env: { ...env, ...r.extra } }, null, 2)
}

function codexConfig(r: CodexRow): string {
  const s = (v: string) => JSON.stringify(v)
  return `model_provider = "custom"
model = ${s(r.model)}
model_reasoning_effort = "high"
disable_response_storage = true

[model_providers.custom]
name = ${s(r.provider)}
base_url = ${s(r.baseUrl)}
wire_api = "responses"
requires_openai_auth = true`
}

const claudeRows: ClaudeRow[] = [
  { name: 'Claude 官方', category: 'official', site: 'https://www.anthropic.com/claude-code', baseUrl: '' },
  {
    name: 'Kimi', category: 'cn_official', site: 'https://platform.kimi.com',
    baseUrl: 'https://api.moonshot.cn/anthropic', model: 'kimi-k2.7-code',
  },
  {
    name: 'Kimi For Coding', category: 'cn_official', site: 'https://www.kimi.com/code/',
    baseUrl: 'https://api.kimi.com/coding/', model: 'kimi-for-coding',
    // 端点别名 + 双键钉 256K 压缩窗口,屏蔽远端下发的更小压缩点。
    extra: { CLAUDE_CODE_MAX_CONTEXT_TOKENS: '262144', CLAUDE_CODE_AUTO_COMPACT_WINDOW: '262144' },
  },
  {
    name: '智谱 GLM', category: 'cn_official', site: 'https://open.bigmodel.cn',
    baseUrl: 'https://open.bigmodel.cn/api/anthropic', model: 'glm-5.1',
  },
  {
    name: '智谱 GLM 国际', category: 'cn_official', site: 'https://z.ai',
    baseUrl: 'https://api.z.ai/api/anthropic', model: 'glm-5.1',
  },
  {
    name: 'DeepSeek', category: 'cn_official', site: 'https://platform.deepseek.com',
    baseUrl: 'https://api.deepseek.com/anthropic', model: 'deepseek-v4-pro',
  },
  {
    name: 'MiniMax', category: 'cn_official', site: 'https://platform.minimaxi.com',
    baseUrl: 'https://api.minimaxi.com/anthropic', model: 'MiniMax-M2.7',
    extra: { API_TIMEOUT_MS: '3000000', CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: 1 },
  },
  {
    name: 'LongCat', category: 'cn_official', site: 'https://longcat.chat/platform',
    baseUrl: 'https://api.longcat.chat/anthropic', model: 'LongCat-2.0',
    extra: { CLAUDE_CODE_MAX_OUTPUT_TOKENS: '131072', CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: 1 },
  },
  {
    name: '火山 Coding Plan', category: 'cn_official', site: 'https://www.volcengine.com/activity/codingplan',
    baseUrl: 'https://ark.cn-beijing.volces.com/api/coding', model: 'ark-code-latest',
  },
  {
    name: '百炼 For Coding', category: 'cn_official', site: 'https://bailian.console.aliyun.com',
    baseUrl: 'https://coding.dashscope.aliyuncs.com/apps/anthropic',
  },
  {
    name: 'ModelScope', category: 'aggregator', site: 'https://modelscope.cn',
    baseUrl: 'https://api-inference.modelscope.cn', model: 'ZhipuAI/GLM-5.2',
  },
  {
    name: 'SiliconFlow', category: 'aggregator', site: 'https://siliconflow.cn',
    baseUrl: 'https://api.siliconflow.cn', model: 'Pro/MiniMaxAI/MiniMax-M2.5',
  },
  {
    name: 'OpenRouter', category: 'aggregator', site: 'https://openrouter.ai',
    baseUrl: 'https://openrouter.ai/api', model: 'anthropic/claude-sonnet-5',
  },
  {
    name: 'AiHubMix', category: 'aggregator', site: 'https://aihubmix.com',
    baseUrl: 'https://aihubmix.com', keyField: 'ANTHROPIC_API_KEY',
  },
  {
    name: 'PackyCode', category: 'third_party', site: 'https://www.packyapi.ai',
    baseUrl: 'https://www.packyapi.ai',
  },
]

const codexRows: CodexRow[] = [
  {
    name: 'Kimi', category: 'cn_official', site: 'https://platform.kimi.com',
    provider: 'kimi', baseUrl: 'https://api.moonshot.cn/v1', model: 'kimi-k2.7-code',
  },
  {
    name: 'Kimi For Coding', category: 'cn_official', site: 'https://www.kimi.com/code/',
    provider: 'kimi_coding', baseUrl: 'https://api.kimi.com/coding/v1', model: 'kimi-for-coding',
  },
  {
    name: '智谱 GLM', category: 'cn_official', site: 'https://open.bigmodel.cn',
    provider: 'zhipu_glm', baseUrl: 'https://open.bigmodel.cn/api/coding/paas/v4', model: 'glm-5.2',
  },
  {
    name: 'DeepSeek', category: 'cn_official', site: 'https://platform.deepseek.com',
    provider: 'deepseek', baseUrl: 'https://api.deepseek.com', model: 'deepseek-v4-flash',
  },
  {
    name: 'MiniMax', category: 'cn_official', site: 'https://platform.minimaxi.com',
    provider: 'minimax', baseUrl: 'https://api.minimaxi.com/v1', model: 'MiniMax-M3',
  },
  {
    name: 'LongCat', category: 'cn_official', site: 'https://longcat.chat/platform',
    provider: 'longcat', baseUrl: 'https://api.longcat.chat/openai/v1', model: 'LongCat-2.0',
  },
  {
    name: '火山 Coding Plan', category: 'cn_official', site: 'https://www.volcengine.com/activity/codingplan',
    provider: 'ark_codingplan', baseUrl: 'https://ark.cn-beijing.volces.com/api/coding/v3', model: 'ark-code-latest',
  },
  {
    name: 'ModelScope', category: 'aggregator', site: 'https://modelscope.cn',
    provider: 'modelscope', baseUrl: 'https://api-inference.modelscope.cn/v1', model: 'ZhipuAI/GLM-5.2',
  },
  {
    name: 'SiliconFlow', category: 'aggregator', site: 'https://siliconflow.cn',
    provider: 'siliconflow', baseUrl: 'https://api.siliconflow.cn/v1', model: 'Pro/MiniMaxAI/MiniMax-M2.5',
  },
  {
    name: 'OpenRouter', category: 'aggregator', site: 'https://openrouter.ai',
    provider: 'openrouter', baseUrl: 'https://openrouter.ai/api/v1', model: 'gpt-5.6-sol',
  },
  {
    name: 'PackyCode', category: 'third_party', site: 'https://www.packyapi.ai',
    provider: 'packycode', baseUrl: 'https://www.packyapi.ai/v1', model: 'gpt-5.6-sol',
  },
]

export const AI_PRESETS: AiPreset[] = [
  ...claudeRows.map<AiPreset>((r) => ({
    app: 'claude', name: r.name, category: r.category, websiteUrl: r.site, config: claudeConfig(r),
  })),
  // OpenAI 官方走 ChatGPT 登录,config.toml 与 auth.json 都留空:切过去就是清掉
  // 第三方那套 provider 配置,登录态本身在 auth.json 里,写空 auth 不会碰它。
  {
    app: 'codex', name: 'OpenAI 官方', category: 'official',
    websiteUrl: 'https://chatgpt.com/codex', config: '', auth: '{}',
  },
  ...codexRows.map<AiPreset>((r) => ({
    app: 'codex', name: r.name, category: r.category, websiteUrl: r.site,
    config: codexConfig(r),
    auth: JSON.stringify({ OPENAI_API_KEY: API_KEY_PLACEHOLDER }, null, 2),
  })),
]

export function presetsFor(app: AiApp): AiPreset[] {
  return AI_PRESETS.filter((p) => p.app === app)
}

// fillKey 把占位符换成真实 key。只在保存时替换一次,编辑框里始终留着占位符 ——
// 双向同步 key 和正文很容易打起来,用户想手改哪个字段都行。
export function fillKey(text: string, key: string): string {
  return text.split(API_KEY_PLACEHOLDER).join(key)
}
