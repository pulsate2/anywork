import { describe, expect, it } from 'vitest'
import { conversionProviders, convertAiConfig, convertBaseUrl } from '../aiConvert'
import type { AiApp } from '@/api/client'

const cc = {
  app: 'claude' as const, name: '代理', config: {
    env: { ANTHROPIC_BASE_URL: 'https://proxy.test/api/', ANTHROPIC_AUTH_TOKEN: 'test-key', ANTHROPIC_MODEL: 'custom-model' },
    permissions: { allow: ['Bash(*)'] },
  },
}
const codex = {
  app: 'codex' as const, name: '代理', config: {
    auth: { OPENAI_API_KEY: 'test-key', tokens: { access_token: 'do-not-copy' } },
    config: 'model_provider = "chosen"\nmodel = "custom-model"\n[model_providers.unused]\nbase_url = "https://wrong.test/v1"\n[model_providers.chosen]\nbase_url = "https://proxy.test/api/v1"\nwire_api = "responses"',
  },
}
const pi = {
  app: 'pi' as const, name: '代理', config: {
    models: { providers: { chosen: { baseUrl: 'https://proxy.test/api/v1', api: 'openai-responses', models: [{ id: 'custom-model' }, { id: 'second-model' }] } } },
    auth: { chosen: { type: 'api_key', key: 'test-key' }, other: { type: 'oauth', access: 'do-not-copy' } },
  },
}

describe('跨客户端配置转换', () => {
  it('CC → Codex:加 /v1,迁移模型与 Key,不复制客户端专属设置', () => {
    const draft = convertAiConfig(cc, 'codex').config as any
    expect(draft.auth).toEqual({ OPENAI_API_KEY: 'test-key' })
    expect(draft.config).toContain('base_url = "https://proxy.test/api/v1"')
    expect(draft.config).toContain('wire_api = "responses"')
    expect(draft.config).toContain('model = "custom-model"')
    expect(draft.config).not.toContain('model_reasoning_effort')
    expect(JSON.stringify(draft)).not.toContain('Bash(*)')
  })

  it('CC → Pi:默认 Responses;选择 Messages 不加 /v1', () => {
    const draft = convertAiConfig(cc, 'pi').config as any
    expect(draft.models.providers.custom).toMatchObject({ baseUrl: 'https://proxy.test/api/v1', api: 'openai-responses', models: [{ id: 'custom-model' }] })
    expect(draft.auth.custom).toEqual({ type: 'api_key', key: 'test-key' })
    const messages = convertAiConfig(cc, 'pi', { piApi: 'anthropic-messages' }).config as any
    expect(messages.models.providers.custom).toMatchObject({ baseUrl: 'https://proxy.test/api', api: 'anthropic-messages' })
  })

  it('Codex → CC:按激活的供应商读 URL,去 /v1,钉四个模型别名', () => {
    const draft = convertAiConfig(codex, 'claude').config as any
    expect(draft.env).toEqual({
      ANTHROPIC_BASE_URL: 'https://proxy.test/api', ANTHROPIC_AUTH_TOKEN: 'test-key',
      ANTHROPIC_MODEL: 'custom-model', ANTHROPIC_DEFAULT_HAIKU_MODEL: 'custom-model',
      ANTHROPIC_DEFAULT_SONNET_MODEL: 'custom-model', ANTHROPIC_DEFAULT_OPUS_MODEL: 'custom-model',
    })
    expect(JSON.stringify(draft)).not.toContain('do-not-copy')
  })

  it('Codex → Pi Responses:URL 和 Key 保持对应,不重复加 /v1', () => {
    const draft = convertAiConfig(codex, 'pi').config as any
    expect(draft.models.providers.custom.baseUrl).toBe('https://proxy.test/api/v1')
    expect(draft.auth.custom.key).toBe('test-key')
  })

  it.each(['claude', 'codex'] as AiApp[])('Pi Responses → %s:迁移所选供应商第一个模型', (target) => {
    const draft = convertAiConfig(pi, target).config as any
    if (target === 'claude') {
      expect(draft.env.ANTHROPIC_BASE_URL).toBe('https://proxy.test/api')
      expect(draft.env.ANTHROPIC_MODEL).toBe('custom-model')
      expect(draft.env.ANTHROPIC_AUTH_TOKEN).toBe('test-key')
    } else {
      expect(draft.config).toContain('base_url = "https://proxy.test/api/v1"')
      expect(draft.config).toContain('model = "custom-model"')
      expect(draft.auth.OPENAI_API_KEY).toBe('test-key')
    }
  })

  it('多供应商 Pi 必须选择,不能拿第一份端点配另一份 Key', () => {
    const multi = JSON.parse(JSON.stringify(pi))
    multi.config.models.providers.other = { name: '另一家', baseUrl: 'https://other.test', api: 'anthropic-messages', models: [{ id: 'other-model' }] }
    multi.config.auth.other = { type: 'api_key', key: 'other-key' }
    expect(conversionProviders(multi)).toEqual([{ value: 'chosen', label: 'chosen' }, { value: 'other', label: '另一家' }])
    expect(() => convertAiConfig(multi, 'codex')).toThrow('请选择')
    const draft = convertAiConfig(multi, 'codex', { providerId: 'other' }).config as any
    expect(draft.auth.OPENAI_API_KEY).toBe('other-key')
    expect(draft.config).toContain('base_url = "https://other.test/v1"')
    expect(draft.config).toContain('model = "other-model"')
  })

  it('TOML 支持单双引号、带引号的 provider 名、注释和 CRLF', () => {
    const source = { ...codex, config: { auth: {}, config: `model_provider = 'a.b' # chosen\r\nmodel = 'name'\r\n[model_providers."a.b"] # table\r\nbase_url = 'https://proxy.test/v1' # url\r\nexperimental_bearer_token = 'token'` } }
    const draft = convertAiConfig(source, 'claude').config as any
    expect(draft.env).toMatchObject({ ANTHROPIC_BASE_URL: 'https://proxy.test', ANTHROPIC_AUTH_TOKEN: 'token', ANTHROPIC_MODEL: 'name' })
  })

  it('CC 仅有 API_KEY 时也能迁移', () => {
    const source = { ...cc, config: { env: { ANTHROPIC_BASE_URL: 'https://proxy.test', ANTHROPIC_API_KEY: 'api-key' } } }
    expect((convertAiConfig(source, 'codex').config as any).auth.OPENAI_API_KEY).toBe('api-key')
  })

  it('OAuth 不当作 API Key,缺少 Key/模型时明确提示', () => {
    const source = { ...codex, config: { ...codex.config, auth: { tokens: { access_token: 'do-not-copy' } }, config: 'model_provider = "custom"\n[model_providers.custom]\nbase_url = "https://proxy.test/v1"' } }
    const draft = convertAiConfig(source, 'pi')
    expect((draft.config as any).auth).toEqual({})
    expect(draft.warnings).toHaveLength(2)
    expect(JSON.stringify(draft)).not.toContain('do-not-copy')
  })

  it('转换不修改任何源配置', () => {
    for (const source of [cc, codex, pi]) {
      const before = JSON.stringify(source)
      for (const target of ['claude', 'codex', 'pi'] as AiApp[]) convertAiConfig(source, target)
      expect(JSON.stringify(source)).toBe(before)
    }
  })

  it('不默默把无法转换的官方登录/非法配置变成错误目标配置', () => {
    expect(() => convertAiConfig({ ...cc, config: { env: {} } }, 'pi')).toThrow('OAuth')
    expect(() => convertAiConfig({ ...cc, config: null }, 'codex')).toThrow('JSON')
    expect(() => convertAiConfig({ ...cc, config: { env: { ANTHROPIC_BASE_URL: 'not-a-url' } } }, 'codex')).toThrow('有效')
    expect(() => convertAiConfig({ ...codex, config: { auth: {}, config: 'model_provider = "missing"\n[model_providers.other]\nbase_url = "https://wrong.test"' } }, 'claude')).toThrow('自定义接口地址')
  })
})

describe('/v1 边界处理', () => {
  it.each([
    ['https://x.test', 'https://x.test/v1'],
    ['https://x.test/', 'https://x.test/v1'],
    ['https://x.test/v1/', 'https://x.test/v1'],
    ['https://x.test/anthropic/', 'https://x.test/anthropic/v1'],
    ['https://x.test/api?token=demo#fragment', 'https://x.test/api/v1?token=demo#fragment'],
    ['https://x.test/api/v1?x=1', 'https://x.test/api/v1?x=1'],
  ])('Messages → Responses: %s', (url, expected) => {
    expect(convertBaseUrl(url, 'anthropic-messages', 'openai-responses')).toBe(expected)
  })
  it.each([
    ['https://x.test/v1/', 'https://x.test'],
    ['https://x.test/api/v1?x=1', 'https://x.test/api?x=1'],
    ['https://x.test/v10', 'https://x.test/v10'],
    ['https://x.test/v1/proxy', 'https://x.test/v1/proxy'],
  ])('Responses → Messages: %s', (url, expected) => {
    expect(convertBaseUrl(url, 'openai-responses', 'anthropic-messages')).toBe(expected)
  })
  it('Responses 之间保留供应商的其他版本路径', () => {
    expect(convertBaseUrl('https://x.test/api/v4', 'openai-responses', 'openai-responses')).toBe('https://x.test/api/v4')
  })
  it('拒绝非 HTTP 地址', () => {
    expect(() => convertBaseUrl('file:///tmp/config', 'anthropic-messages', 'openai-responses')).toThrow('http')
  })
})
