// 与预设一样在前端生成可编辑的配置草稿,保存复用现有 Create API。
// 只迁移端点/API Key/模型;不跨客户端复制 OAuth 或专属设置。
import type { AiApp, AiProvider } from '@/api/client'

export type ConversionApi = 'anthropic-messages' | 'openai-responses' | 'openai-completions'

interface PortableConfig {
  baseUrl: string
  key: string
  model: string
  api: ConversionApi
}

export interface ConversionDraft {
  config: unknown
  warnings: string[]
}

function object(value: unknown): Record<string, any> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('源配置不是有效的 JSON 对象')
  }
  return value as Record<string, any>
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

// 读取转换需要的 TOML 字符串,不把别的 provider 的第一条 base_url 当成当前值。
// 支持单双引号、注释、CRLF 与有引号的表名;不支持的字段语法明确报错。
function codexTables(config: string): Map<string, Record<string, string>> {
  const tables = new Map<string, Record<string, string>>([['', {}]])
  let table = ''
  for (const line of config.split(/\r?\n/)) {
    const header = line.match(/^\s*\[\s*(.*?)\s*\]\s*(?:#.*)?$/)
    if (header) {
      const provider = header[1].match(/^model_providers\.("(?:\\.|[^"\\])*"|'[^']*'|[\w-]+)$/)
      table = provider ? `provider:${tomlString(provider[1])}` : header[1]
      if (!tables.has(table)) tables.set(table, {})
      continue
    }
    const field = line.match(/^\s*(model|model_provider|base_url|wire_api|experimental_bearer_token)\s*=\s*(.*?)\s*$/)
    if (!field || (table !== '' && !table.startsWith('provider:'))) continue
    const value = field[2].match(/^("(?:\\.|[^"\\])*"|'[^']*')\s*(?:#.*)?$/)
    if (!value) throw new Error(`无法转换 Codex 的 ${field[1]} 字段,请使用单行字符串`)
    tables.get(table)![field[1]] = tomlString(value[1])
  }
  return tables
}

function tomlString(value: string): string {
  if (value.startsWith('"')) {
    try { return JSON.parse(value) } catch { throw new Error('Codex TOML 字符串无法解析') }
  }
  if (value.startsWith("'")) return value.slice(1, -1)
  return value
}

export function conversionProviders(source: Pick<AiProvider, 'app' | 'config'>): { value: string; label: string }[] {
  if (source.app !== 'pi') return []
  const models = object(object(source.config).models ?? {})
  return Object.entries(object(models.providers ?? {})).map(([id, value]) => ({
    value: id, label: text(object(value).name) || id,
  }))
}

function extract(source: Pick<AiProvider, 'app' | 'config'>, providerId?: string): PortableConfig {
  const cfg = object(source.config)
  if (source.app === 'claude') {
    const env = object(cfg.env ?? {})
    return {
      baseUrl: text(env.ANTHROPIC_BASE_URL),
      key: text(env.ANTHROPIC_AUTH_TOKEN) || text(env.ANTHROPIC_API_KEY),
      model: text(env.ANTHROPIC_MODEL) || text(env.ANTHROPIC_DEFAULT_SONNET_MODEL),
      api: 'anthropic-messages',
    }
  }
  if (source.app === 'codex') {
    const tables = codexTables(text(cfg.config))
    const root = tables.get('')!
    const providers = [...tables.keys()].filter((name) => name.startsWith('provider:'))
    const id = root.model_provider ? `provider:${root.model_provider}` : providers.length === 1 ? providers[0] : ''
    const provider = tables.get(id) ?? {}
    if (!root.model_provider && providers.length > 1) throw new Error('Codex 配置包含多个供应商,请先指定 model_provider')
    const wire = provider.wire_api || 'responses'
    if (wire !== 'responses' && wire !== 'chat') throw new Error(`不支持转换 Codex 的 ${wire} 协议`)
    return {
      baseUrl: provider.base_url || '',
      key: text(object(cfg.auth ?? {}).OPENAI_API_KEY) || provider.experimental_bearer_token || '',
      model: root.model || '',
      api: wire === 'chat' ? 'openai-completions' : 'openai-responses',
    }
  }
  const providers = object(object(cfg.models ?? {}).providers ?? {})
  const ids = Object.keys(providers)
  if (!providerId && ids.length > 1) throw new Error('Pi 配置包含多个供应商,请选择要转换的供应商')
  const id = providerId || ids[0]
  if (!id || !(id in providers)) throw new Error('Pi 配置中没有可转换的自定义供应商')
  const provider = object(providers[id])
  const auth = object(cfg.auth ?? {})
  const credential = auth[id] ? object(auth[id]) : {}
  const api = text(provider.api) || 'openai-completions'
  if (!['anthropic-messages', 'openai-responses', 'openai-completions'].includes(api)) {
    throw new Error(`不支持转换 Pi 的 ${api} 协议`)
  }
  // apiKey 还可以是环境变量名或命令;这种动态凭据不伪装成可用的静态 Key。
  const inlineKey = text(provider.apiKey)
  const staticInlineKey = inlineKey && !/^[A-Z][A-Z0-9_]*$/.test(inlineKey) && !inlineKey.startsWith('!') && !inlineKey.includes('${') ? inlineKey : ''
  return {
    baseUrl: text(provider.baseUrl),
    key: credential.type === 'api_key' ? text(credential.key) : credential.type === 'oauth' ? '' : staticInlineKey,
    model: Array.isArray(provider.models) ? text(provider.models[0]?.id) : '',
    api: api as ConversionApi,
  }
}

// 只增删路径末尾的 /v1,保留反代前缀、查询参数与 fragment。不重复加 /v1,
// 不替换 /anthropic、/v4 等供应商专用路径(协议是否支持需用户确认)。
export function convertBaseUrl(baseUrl: string, sourceApi: ConversionApi, targetApi: ConversionApi): string {
  const value = baseUrl.trim()
  let url: URL
  try { url = new URL(value) } catch { throw new Error('请先给源配置填写有效的接口地址') }
  if (!['http:', 'https:'].includes(url.protocol)) throw new Error('接口地址必须使用 http 或 https')
  const split = value.search(/[?#]/)
  let path = (split < 0 ? value : value.slice(0, split)).replace(/\/+$/, '')
  const suffix = split < 0 ? '' : value.slice(split)
  if (targetApi === 'anthropic-messages' && sourceApi !== 'anthropic-messages') {
    path = path.replace(/\/v1$/, '')
  } else if (targetApi !== 'anthropic-messages' && sourceApi === 'anthropic-messages' && !path.endsWith('/v1')) {
    path += '/v1'
  }
  return path + suffix
}

export function convertAiConfig(
  source: Pick<AiProvider, 'app' | 'config' | 'name'>,
  target: AiApp,
  options: { piApi?: ConversionApi; providerId?: string } = {},
): ConversionDraft {
  if (!['claude', 'codex', 'pi'].includes(source.app)) throw new Error('不支持的源客户端')
  if (!['claude', 'codex', 'pi'].includes(target)) throw new Error('不支持的目标客户端')
  const fields = extract(source, options.providerId)
  if (!fields.baseUrl) throw new Error('源配置没有自定义接口地址;官方 OAuth 登录态不能直接跨客户端转换')
  const api: ConversionApi = target === 'claude' ? 'anthropic-messages' : target === 'codex' ? 'openai-responses' : options.piApi || 'openai-responses'
  const baseUrl = convertBaseUrl(fields.baseUrl, fields.api, api)
  const warnings: string[] = []
  const key = fields.key.includes('${') || fields.key.startsWith('!') ? '' : fields.key
  if (!key) warnings.push('未找到可迁移的 API Key;OAuth、环境变量和命令凭据不会复制,请在目标配置中补填。')
  if (!fields.model) warnings.push('源配置未指定模型,请确认目标客户端的默认模型或补填模型名。')
  const model = fields.model
  if (target === 'claude') {
    const env: Record<string, string> = { ANTHROPIC_BASE_URL: baseUrl }
    if (key) env.ANTHROPIC_AUTH_TOKEN = key
    if (model) for (const k of ['ANTHROPIC_MODEL', 'ANTHROPIC_DEFAULT_HAIKU_MODEL', 'ANTHROPIC_DEFAULT_SONNET_MODEL', 'ANTHROPIC_DEFAULT_OPUS_MODEL']) env[k] = model
    return { config: { env }, warnings }
  }
  if (target === 'codex') {
    const lines = ['model_provider = "custom"']
    if (model) lines.push(`model = ${JSON.stringify(model)}`)
    lines.push('', '[model_providers.custom]', `name = ${JSON.stringify(source.name)}`,
      `base_url = ${JSON.stringify(baseUrl)}`, 'wire_api = "responses"', 'requires_openai_auth = true')
    return { config: { config: lines.join('\n'), auth: key ? { OPENAI_API_KEY: key } : {} }, warnings }
  }
  const provider: Record<string, unknown> = { name: source.name, baseUrl, api }
  if (model) provider.models = [{ id: model }]
  return {
    config: { models: { providers: { custom: provider } }, auth: key ? { custom: { type: 'api_key', key } } : {} },
    warnings,
  }
}
