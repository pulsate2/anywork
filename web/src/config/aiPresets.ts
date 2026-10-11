// AI 供应商预设。跟 cc-switch 一样只放在前端:预设只是"填表模板",选完就变成
// 一份普通配置存进库里,以后改预设表也不会动到用户已经保存的配置。
import type { AiApp } from '@/api/client'

// pi 的接口协议可选项。openai-completions 覆盖大多数国产/聚合端点;要走 Claude
// 原生协议选 anthropic-messages(baseUrl 换成供应商的 /anthropic 端点即可,key
// 照常放 auth.json,pi 会当成 x-api-key 发出去)。取值对齐 pi-ai 的 KnownApi。
export interface PiApiChoice { value: string; label: string }
export const PI_API_CHOICES: PiApiChoice[] = [
  { value: 'openai-completions', label: 'OpenAI 兼容(chat/completions)' },
  { value: 'anthropic-messages', label: 'Claude(Anthropic Messages)' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
]

export interface AiPreset {
  app: AiApp
  name: string
  category: string
  websiteUrl: string
  config: string // claude: settings.json 文本;codex: config.toml 文本;pi: models.json 文本
  auth?: string // codex / pi:auth.json 文本
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
  model?: string // 有值就连 haiku/sonnet/opus 一起钉住;留空只给输入框留位,不写模型字段
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
  wireApi?: string // 默认 responses;OpenAI 兼容端点多半只有 /chat/completions
}

interface PiRow {
  name: string
  category: string
  site: string
  provider: string // auth.json 与 models.json 共用的 providerId
  baseUrl: string
  model: string
  api?: string // 默认 openai-completions;大多数国产端点都是 OpenAI /chat/completions 兼容
}

// 四个模型字段一起钉:CLI 会按 haiku/sonnet/opus 别名挑模型,只写 ANTHROPIC_MODEL
// 的话小任务还是会去要 haiku,第三方端点通常没这个模型。
const CLAUDE_MODEL_KEYS = [
  'ANTHROPIC_MODEL',
  'ANTHROPIC_DEFAULT_HAIKU_MODEL',
  'ANTHROPIC_DEFAULT_SONNET_MODEL',
  'ANTHROPIC_DEFAULT_OPUS_MODEL',
]

function claudeConfig(r: ClaudeRow): string {
  if (!r.baseUrl) return JSON.stringify({ env: {} }, null, 2)
  const env: Record<string, string | number> = { ANTHROPIC_BASE_URL: r.baseUrl }
  env[r.keyField || 'ANTHROPIC_AUTH_TOKEN'] = '' // 空着占位,Key 从"API Key"框写进来
  if (r.model) for (const k of CLAUDE_MODEL_KEYS) env[k] = r.model
  return JSON.stringify({ env: { ...env, ...r.extra } }, null, 2)
}

// codex 的 config.toml 原样当字符串存。模型 / base_url 留空的行直接不写 —— 省得保存时
// 落一个 model = "" 的坏配置;用户在"模型""接口地址"框里填了,writeField 会把行补回来。
function codexConfig(r: CodexRow): string {
  const s = (v: string) => JSON.stringify(v)
  const head = ['model_provider = "custom"']
  if (r.model) head.push(`model = ${s(r.model)}`)
  head.push('model_reasoning_effort = "high"', 'disable_response_storage = true', '',
    '[model_providers.custom]', `name = ${s(r.provider)}`)
  if (r.baseUrl) head.push(`base_url = ${s(r.baseUrl)}`)
  head.push(`wire_api = ${s(r.wireApi || 'responses')}`, 'requires_openai_auth = true')
  return head.join('\n')
}

// pi 的 models.json:一个自定义 provider 挂一份模型目录。baseUrl / models 留空就不写
// 该键(provider-composer 允许只给 baseUrl、或连 baseUrl 都不给让 pi 自己发现)。
function piConfig(r: PiRow): string {
  const prov: Record<string, unknown> = { name: r.name }
  if (r.baseUrl) prov.baseUrl = r.baseUrl
  prov.api = r.api || 'openai-completions'
  if (r.model) prov.models = [{ id: r.model }]
  return JSON.stringify({ providers: { [r.provider]: prov } }, null, 2)
}

// pi 的 auth.json:providerId → api_key。key 空着占位,从"API Key"框写进来。
function piAuth(r: PiRow): string {
  return JSON.stringify({ [r.provider]: { type: 'api_key', key: '' } }, null, 2)
}

const claudeRows: ClaudeRow[] = [
  { name: 'Claude 官方', category: 'official', site: 'https://www.anthropic.com/claude-code', baseUrl: '' },
  // 自定义紧跟官方:端点/模型/Key 三个输入框都在弹窗里,不用手改 JSON。
  {
    name: '自定义', category: 'custom', site: '',
    baseUrl: '', model: '',
  },
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
  // 自定义端点默认走 responses 协议;OpenAI 兼容的 /chat/completions 端点需要用户手动改回 chat。
  {
    name: '自定义', category: 'custom', site: '',
    provider: 'custom', baseUrl: '', model: '', wireApi: 'responses',
  },
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

// pi 走 OpenAI /chat/completions 兼容端点(api: openai-completions),与 codex 的
// 一组国产/聚合端点基本重合;自定义行用占位符,端点/模型/Key 在弹窗里填。
const piRows: PiRow[] = [
  {
    name: '自定义', category: 'custom', site: '',
    provider: 'custom', baseUrl: '', model: '',
  },
  {
    name: 'Kimi', category: 'cn_official', site: 'https://platform.kimi.com',
    provider: 'kimi', baseUrl: 'https://api.moonshot.cn/v1', model: 'kimi-k2.7-code',
  },
  {
    name: '智谱 GLM', category: 'cn_official', site: 'https://open.bigmodel.cn',
    provider: 'zhipu_glm', baseUrl: 'https://open.bigmodel.cn/api/paas/v4', model: 'glm-5.2',
  },
  {
    name: 'DeepSeek', category: 'cn_official', site: 'https://platform.deepseek.com',
    provider: 'deepseek', baseUrl: 'https://api.deepseek.com/v1', model: 'deepseek-v4-flash',
  },
  {
    name: 'MiniMax', category: 'cn_official', site: 'https://platform.minimaxi.com',
    provider: 'minimax', baseUrl: 'https://api.minimaxi.com/v1', model: 'MiniMax-M3',
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
    provider: 'openrouter', baseUrl: 'https://openrouter.ai/api/v1', model: 'anthropic/claude-sonnet-5',
  },
]

export const AI_PRESETS: AiPreset[] = [
  ...claudeRows.map<AiPreset>((r) => ({
    app: 'claude', name: r.name, category: r.category, websiteUrl: r.site,
    config: claudeConfig(r),
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
    auth: JSON.stringify({ OPENAI_API_KEY: '' }, null, 2),
  })),
  ...piRows.map<AiPreset>((r) => ({
    app: 'pi', name: r.name, category: r.category, websiteUrl: r.site,
    config: piConfig(r), auth: piAuth(r),
  })),
]

export function presetsFor(app: AiApp): AiPreset[] {
  return AI_PRESETS.filter((p) => p.app === app)
}

// 下面是便捷输入框 ↔ 配置正文的桥:readFields 打开弹窗时从正文解析出当前值回填输入框,
// writeField 改一个框就把那个字段直接写回正文 JSON/TOML。正文始终是唯一真相源 ——
// 不再埋占位符,新建(套预设)和编辑已有配置走同一套。

export type FieldName = 'baseUrl' | 'key' | 'model' | 'api'

// readFields 从配置正文里读出四个便捷字段。解析坏了就返回已填的部分,绝不抛。
// config:claude=settings.json / pi=models.json / codex=config.toml;auth:codex / pi 的 auth.json。
export function readFields(app: AiApp, config: string, auth: string): Record<FieldName, string> {
  const out: Record<FieldName, string> = { baseUrl: '', key: '', model: '', api: '' }
  try {
    if (app === 'claude') {
      const env = (safeParse(config)?.env ?? {}) as Record<string, string>
      out.baseUrl = env.ANTHROPIC_BASE_URL || ''
      out.key = env.ANTHROPIC_AUTH_TOKEN || env.ANTHROPIC_API_KEY || ''
      out.model = env.ANTHROPIC_MODEL || ''
    } else if (app === 'pi') {
      const prov = piFirst(config)
      out.baseUrl = prov?.baseUrl || ''
      out.api = prov?.api || 'openai-completions'
      out.model = prov?.models?.[0]?.id || ''
      const id = piFirstId(config)
      out.key = (id && safeParse(auth)?.[id]?.key) || ''
    } else {
      out.baseUrl = matchToml(config, 'base_url')
      out.model = matchToml(config, 'model')
      out.key = safeParse(auth)?.OPENAI_API_KEY || ''
    }
  } catch { /* 落到这儿就用已填好的那几个 */ }
  return out
}

// writeField 把一个便捷字段写回正文,返回新的 { config, auth }。空值一律删字段 / 删行
// (等于不钉,让 CLI 走默认),绝不写空字符串。只改动涉及的那份文本,另一份原样返回。
export function writeField(
  app: AiApp, field: FieldName, value: string, config: string, auth: string,
): { config: string; auth: string } {
  const v = value.trim()
  if (app === 'claude') return { config: writeClaude(field, v, config), auth }
  if (app === 'pi') return writePi(field, v, config, auth)
  return writeCodex(field, v, config, auth)
}

function safeParse(text: string): any {
  try {
    const o = JSON.parse(text || '{}')
    return o && typeof o === 'object' ? o : null
  } catch { return null }
}

function piProviders(config: string): Record<string, any> | null {
  const p = safeParse(config)?.providers
  return p && typeof p === 'object' ? p : null
}
function piFirstId(config: string): string {
  const p = piProviders(config)
  return p ? (Object.keys(p)[0] ?? '') : ''
}
function piFirst(config: string): any {
  const p = piProviders(config)
  return p ? (Object.values(p)[0] ?? null) : null
}

function setOrDel(obj: Record<string, unknown>, key: string, v: string): void {
  if (v) obj[key] = v
  else delete obj[key]
}

function writeClaude(field: FieldName, v: string, config: string): string {
  const obj = safeParse(config) ?? {}
  const env: Record<string, string | number> =
    obj.env && typeof obj.env === 'object' ? obj.env : (obj.env = {})
  if (field === 'baseUrl') setOrDel(env, 'ANTHROPIC_BASE_URL', v)
  else if (field === 'model') for (const k of CLAUDE_MODEL_KEYS) setOrDel(env, k, v)
  else if (field === 'key') {
    // 少数供应商只认 ANTHROPIC_API_KEY:正文里已经是这个名字就续写它,否则用 AUTH_TOKEN。
    const name = 'ANTHROPIC_API_KEY' in env && !('ANTHROPIC_AUTH_TOKEN' in env)
      ? 'ANTHROPIC_API_KEY' : 'ANTHROPIC_AUTH_TOKEN'
    setOrDel(env, name, v)
  }
  return JSON.stringify(obj, null, 2)
}

function writePi(
  field: FieldName, v: string, config: string, auth: string,
): { config: string; auth: string } {
  if (field === 'key') {
    const a = safeParse(auth) ?? {}
    const id = piFirstId(config) || 'custom'
    if (v) a[id] = { type: 'api_key', key: v }
    else delete a[id]
    return { config, auth: JSON.stringify(a, null, 2) }
  }
  const models = safeParse(config) ?? {}
  const providers: Record<string, any> =
    models.providers && typeof models.providers === 'object'
      ? models.providers : (models.providers = {})
  let id = Object.keys(providers)[0]
  if (!id) { id = 'custom'; providers[id] = { name: '自定义' } }
  const prov = providers[id]
  if (field === 'baseUrl') setOrDel(prov, 'baseUrl', v)
  else if (field === 'api') prov.api = v || 'openai-completions'
  else if (field === 'model') { if (v) prov.models = [{ id: v }]; else delete prov.models }
  return { config: JSON.stringify(models, null, 2), auth }
}

function writeCodex(
  field: FieldName, v: string, config: string, auth: string,
): { config: string; auth: string } {
  if (field === 'key') {
    const a = safeParse(auth) ?? {}
    setOrDel(a, 'OPENAI_API_KEY', v)
    return { config, auth: JSON.stringify(a, null, 2) }
  }
  if (field === 'api') return { config, auth } // codex 没有 api 字段
  return { config: setToml(config, field === 'baseUrl' ? 'base_url' : 'model', v), auth }
}

// matchToml 读一行 key = "值"。model 不会误命中 model_provider / model_reasoning_effort ——
// key 后面紧跟的得是空白或 =,下划线不算。
function matchToml(config: string, key: string): string {
  const m = config.match(new RegExp(`^[ \\t]*${key}[ \\t]*=[ \\t]*"([^"]*)"`, 'm'))
  return m ? m[1] : ''
}

// setToml 改一行 key = "值":行在就替换,值空就删行,行不在且有值就补一行
// (base_url 补进 [model_providers.*] 块里,model 补到最前)。
function setToml(config: string, key: string, v: string): string {
  const line = new RegExp(`^([ \\t]*)${key}[ \\t]*=[ \\t]*"[^"]*"[ \\t]*$`, 'm')
  if (line.test(config)) {
    if (v) return config.replace(line, `$1${key} = ${JSON.stringify(v)}`)
    return config.replace(new RegExp(`^[ \\t]*${key}[ \\t]*=[ \\t]*"[^"]*"[ \\t]*\\r?\\n?`, 'm'), '')
  }
  if (!v) return config
  const add = `${key} = ${JSON.stringify(v)}`
  if (key === 'model') return `${add}\n${config}`
  const hdr = config.match(/^\[model_providers\.[^\]]+\][ \t]*$/m)
  if (hdr && hdr.index !== undefined) {
    const at = hdr.index + hdr[0].length
    return `${config.slice(0, at)}\n${add}${config.slice(at)}`
  }
  return config ? `${config}\n${add}` : add
}
