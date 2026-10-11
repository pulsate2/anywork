import { describe, expect, it } from 'vitest'
import { AI_PRESETS, readFields, writeField } from '../aiPresets'

describe('readFields / writeField — claude', () => {
  const cfg = JSON.stringify({
    env: {
      ANTHROPIC_BASE_URL: 'https://x', ANTHROPIC_AUTH_TOKEN: 'k',
      ANTHROPIC_MODEL: 'm', ANTHROPIC_DEFAULT_HAIKU_MODEL: 'm',
    },
  })

  it('读出 baseUrl / key / model', () => {
    const f = readFields('claude', cfg, '')
    expect(f).toMatchObject({ baseUrl: 'https://x', key: 'k', model: 'm' })
  })

  it('写 model 一次钉四个字段', () => {
    const { config } = writeField('claude', 'model', 'gpt', cfg, '')
    const env = JSON.parse(config).env
    expect(env.ANTHROPIC_MODEL).toBe('gpt')
    expect(env.ANTHROPIC_DEFAULT_HAIKU_MODEL).toBe('gpt')
    expect(env.ANTHROPIC_DEFAULT_SONNET_MODEL).toBe('gpt')
    expect(env.ANTHROPIC_DEFAULT_OPUS_MODEL).toBe('gpt')
  })

  it('空值删字段,不写空串', () => {
    const { config } = writeField('claude', 'baseUrl', '', cfg, '')
    expect('ANTHROPIC_BASE_URL' in JSON.parse(config).env).toBe(false)
  })

  it('正文里是 API_KEY 名就续写 API_KEY', () => {
    const only = JSON.stringify({ env: { ANTHROPIC_API_KEY: '' } })
    const { config } = writeField('claude', 'key', 'sk', only, '')
    const env = JSON.parse(config).env
    expect(env.ANTHROPIC_API_KEY).toBe('sk')
    expect('ANTHROPIC_AUTH_TOKEN' in env).toBe(false)
  })
})

describe('readFields / writeField — pi', () => {
  const cfg = JSON.stringify({
    providers: { kimi: { name: 'Kimi', baseUrl: 'https://k', api: 'openai-completions', models: [{ id: 'm' }] } },
  })
  const auth = JSON.stringify({ kimi: { type: 'api_key', key: 'sk' } })

  it('读出 baseUrl / api / model / key', () => {
    expect(readFields('pi', cfg, auth)).toMatchObject({
      baseUrl: 'https://k', api: 'openai-completions', model: 'm', key: 'sk',
    })
  })

  it('写 model 落到第一个 provider,空值删 models', () => {
    const set = writeField('pi', 'model', 'x', cfg, auth)
    expect(JSON.parse(set.config).providers.kimi.models).toEqual([{ id: 'x' }])
    const del = writeField('pi', 'model', '', cfg, auth)
    expect('models' in JSON.parse(del.config).providers.kimi).toBe(false)
  })

  it('写 key 落到同一个 provId 的 auth', () => {
    const { auth: a } = writeField('pi', 'key', 'new', cfg, auth)
    expect(JSON.parse(a).kimi).toEqual({ type: 'api_key', key: 'new' })
  })

  it('空配置写 baseUrl 自动建 custom provider', () => {
    const { config } = writeField('pi', 'baseUrl', 'https://n', '{}', '{}')
    expect(JSON.parse(config).providers.custom.baseUrl).toBe('https://n')
  })

  it('写 api 切协议', () => {
    const { config } = writeField('pi', 'api', 'anthropic-messages', cfg, auth)
    expect(JSON.parse(config).providers.kimi.api).toBe('anthropic-messages')
  })
})

describe('readFields / writeField — codex', () => {
  const cfg = [
    'model_provider = "custom"',
    'model = "x"',
    'model_reasoning_effort = "high"',
    '',
    '[model_providers.custom]',
    'name = "kimi"',
    'base_url = "https://c"',
    'wire_api = "responses"',
  ].join('\n')
  const auth = JSON.stringify({ OPENAI_API_KEY: 'ok' })

  it('读 base_url / model,不误命中 model_provider', () => {
    const f = readFields('codex', cfg, auth)
    expect(f.baseUrl).toBe('https://c')
    expect(f.model).toBe('x')
    expect(f.key).toBe('ok')
  })

  it('写 model 替换行,空值删行', () => {
    expect(writeField('codex', 'model', 'y', cfg, auth).config).toContain('model = "y"')
    const del = writeField('codex', 'model', '', cfg, auth).config
    expect(del).not.toMatch(/^model = /m)
    expect(del).toContain('model_provider = "custom"') // 不碰别的 model_ 行
  })

  it('base_url 行不存在就插进 provider 块', () => {
    const noUrl = cfg.replace('base_url = "https://c"\n', '')
    const { config } = writeField('codex', 'baseUrl', 'https://n', noUrl, auth)
    expect(config).toContain('[model_providers.custom]\nbase_url = "https://n"')
  })

  it('写 key 落到 auth.json', () => {
    const { auth: a } = writeField('codex', 'key', 'k2', cfg, auth)
    expect(JSON.parse(a).OPENAI_API_KEY).toBe('k2')
  })
})

describe('AI_PRESETS 正文都是能解析的', () => {
  it('claude/pi 的 config、所有 auth 都是合法 JSON', () => {
    for (const p of AI_PRESETS) {
      if (p.app !== 'codex' && p.config) expect(() => JSON.parse(p.config)).not.toThrow()
      if (p.auth) expect(() => JSON.parse(p.auth!)).not.toThrow()
    }
  })

  it('Kimi(claude)预设读得出 baseUrl 与 model、key 为空', () => {
    const kimi = AI_PRESETS.find((p) => p.app === 'claude' && p.name === 'Kimi')!
    const f = readFields('claude', kimi.config, '')
    expect(f.baseUrl).toBe('https://api.moonshot.cn/anthropic')
    expect(f.model).toBe('kimi-k2.7-code')
    expect(f.key).toBe('')
  })
})
