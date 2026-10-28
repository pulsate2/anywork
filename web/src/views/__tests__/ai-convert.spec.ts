import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { NMessageProvider } from 'naive-ui'
import AIView from '@/views/AIView.vue'
import type { AiProvider } from '@/api/client'

const mocks = vi.hoisted(() => ({ list: vi.fn(), create: vi.fn(), update: vi.fn(), switch: vi.fn() }))
vi.mock('@/api/client', () => ({ api: {
  aiProviders: mocks.list,
  aiProviderCreate: mocks.create,
  aiProviderUpdate: mocks.update,
  aiSwitch: mocks.switch,
  aiExportUrl: () => '/api/ai/export',
} }))

const source: AiProvider = {
  id: 'source', app: 'claude', name: '公司代理', category: 'custom', websiteUrl: 'https://proxy.test',
  isCurrent: true, createdAt: '', updatedAt: '',
  config: { env: { ANTHROPIC_BASE_URL: 'https://proxy.test/api', ANTHROPIC_AUTH_TOKEN: 'test-key', ANTHROPIC_MODEL: 'model' } },
}

let wrapper: VueWrapper | undefined
async function mountView() {
  wrapper = mount({
    components: { NMessageProvider, AIView },
    template: '<n-message-provider><AIView /></n-message-provider>',
  })
  await flushPromises()
  return { w: wrapper, vm: wrapper.findComponent(AIView).vm as any }
}

describe('AI 配置转换入口', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.list.mockImplementation(async (app: string) => ({ providers: app === 'claude' ? [JSON.parse(JSON.stringify(source))] : [] }))
    mocks.create.mockResolvedValue({ id: 'converted' })
  })
  afterEach(() => {
    wrapper?.unmount()
    document.body.innerHTML = ''
  })

  it('卡片转换 → 草稿 → 保存为目标客户端;不改原记录、不切换当前配置', async () => {
    const { w, vm } = await mountView()
    await w.find('.ai-convert').trigger('click')
    expect(vm.convertModal).toBe(true)
    expect(vm.convertTarget).toBe('codex')
    vm.generateConversion()
    await flushPromises()
    expect(vm.app).toBe('claude')
    expect(vm.editApp).toBe('codex')
    expect(vm.editId).toBe('')
    expect(vm.baseUrl).toBe('https://proxy.test/api/v1')
    expect(vm.apiKey).toBe('test-key')
    expect(vm.configText).toContain('wire_api = "responses"')
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.update).not.toHaveBeenCalled()
    expect(mocks.switch).not.toHaveBeenCalled()

    await vm.save()
    await flushPromises()
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({
      app: 'codex', name: '公司代理 (Codex)', category: 'custom', websiteUrl: 'https://proxy.test',
      config: { auth: { OPENAI_API_KEY: 'test-key' }, config: expect.stringContaining('base_url = "https://proxy.test/api/v1"') },
    }))
    expect(vm.app).toBe('codex')
    expect(mocks.list).toHaveBeenLastCalledWith('codex')
    expect(mocks.update).not.toHaveBeenCalled()
    expect(mocks.switch).not.toHaveBeenCalled()
    expect(source.config).toMatchObject({ env: { ANTHROPIC_BASE_URL: 'https://proxy.test/api' } })
  })

  it('CC → Pi 可选择协议,取消生成草稿不产生请求', async () => {
    const { vm } = await mountView()
    vm.openConvert(source)
    vm.convertTarget = 'pi'
    vm.convertPiApi = 'anthropic-messages'
    vm.generateConversion()
    await flushPromises()
    expect(vm.editApp).toBe('pi')
    expect(vm.piApi).toBe('anthropic-messages')
    expect(JSON.parse(vm.configText).providers.custom).toMatchObject({ baseUrl: 'https://proxy.test/api', api: 'anthropic-messages' })
    vm.editModal = false
    expect(vm.app).toBe('claude')
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.update).not.toHaveBeenCalled()
    expect(mocks.switch).not.toHaveBeenCalled()
  })

  it('编辑已有 Pi 配置回填不反写,保留多模型及 auth 扩展字段', async () => {
    const { vm } = await mountView()
    const piSource: AiProvider = { ...source, app: 'pi', config: {
      models: { providers: { p: { baseUrl: 'https://proxy.test/v1', api: 'openai-responses', models: [{ id: 'one' }, { id: 'two' }] } } },
      auth: { p: { type: 'api_key', key: 'test-key', custom: 'preserve' } },
    } }
    vm.openEdit(piSource)
    await flushPromises()
    expect(JSON.parse(vm.configText).providers.p.models).toHaveLength(2)
    expect(JSON.parse(vm.authText).p.custom).toBe('preserve')
  })

  it('多供应商 Pi 选择与转换草稿对应的端点和 Key', async () => {
    const { vm } = await mountView()
    const piSource: AiProvider = { ...source, app: 'pi', config: {
      models: { providers: {
        first: { baseUrl: 'https://first.test/v1', api: 'openai-responses', models: [{ id: 'one' }] },
        second: { baseUrl: 'https://second.test/v1', api: 'openai-responses', models: [{ id: 'two' }] },
      } },
      auth: { first: { type: 'api_key', key: 'one-key' }, second: { type: 'api_key', key: 'two-key' } },
    } }
    vm.openConvert(piSource)
    expect(vm.convertProviderOptions).toHaveLength(2)
    vm.convertProvider = 'second'
    vm.generateConversion()
    await flushPromises()
    expect(vm.editApp).toBe('claude')
    expect(vm.baseUrl).toBe('https://second.test')
    expect(vm.apiKey).toBe('two-key')
    expect(vm.modelName).toBe('two')
  })

  it('无法转换时保留选择弹窗,不保存错误配置', async () => {
    const { vm } = await mountView()
    vm.openConvert({ ...source, config: { env: {} } })
    vm.generateConversion()
    await flushPromises()
    expect(vm.convertModal).toBe(true)
    expect(vm.editModal).toBe(false)
    expect(mocks.create).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('没有自定义接口地址')
  })
})
