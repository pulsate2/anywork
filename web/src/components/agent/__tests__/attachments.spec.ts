import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import Composer from '../Composer.vue'
import UserMessage from '../UserMessage.vue'
import type { VueWrapper } from '@vue/test-utils'

const mocks = vi.hoisted(() => ({ upload: vi.fn() }))
vi.mock('@/api/client', () => ({ api: {
  agentUpload: mocks.upload,
  agentFileUrl: (id: string, name: string) => `/api/agent/sessions/${id}/files/${encodeURIComponent(name)}`,
} }))

const SID = 's-files'
const PATH = `/data/agent-files/${SID}/需求 文档.txt`
const MENTION = `@${JSON.stringify(PATH)}`
const live: VueWrapper[] = []
function composer(id = SID) {
  const wrapper = mount(Composer, { props: { running: false, sessionId: id } })
  live.push(wrapper)
  return wrapper
}
async function upload(wrapper: VueWrapper, names: string[]) {
  const input = wrapper.find('input[type="file"]')
  Object.defineProperty(input.element, 'files', {
    value: names.map((name) => new File(['content'], name)), configurable: true,
  })
  await input.trigger('change')
  await flushPromises()
}

describe('上传附件的文件名标签', () => {
  beforeEach(() => { localStorage.clear(); mocks.upload.mockReset(); mocks.upload.mockResolvedValue({ path: PATH }) })
  afterEach(() => { live.splice(0).forEach((wrapper) => wrapper.unmount()) })

  it('输入区仅显示文件名,附件可单独发送,Agent 仍收到完整路径', async () => {
    const wrapper = composer()
    await upload(wrapper, ['需求 文档.txt'])
    expect(wrapper.find('.attachment-tag').text()).toBe('需求 文档.txt')
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('')
    expect(wrapper.text()).not.toContain('/data/agent-files/')
    await wrapper.find('button[title="发送"]').trigger('click')
    expect(wrapper.emitted('send')?.[0]).toEqual([MENTION])
    expect(wrapper.find('.attachment-tag').exists()).toBe(false)
    expect(localStorage.getItem(`agent.draft.${SID}`)).toBeNull()
  })

  it('有正文的多附件消息实际发送时,完整路径位于正文之前并保留正文换行', async () => {
    const secondPath = `/data/agent-files/${SID}/截图.png`
    mocks.upload.mockResolvedValueOnce({ path: PATH }).mockResolvedValueOnce({ path: secondPath })
    const wrapper = composer()
    await wrapper.find('textarea').setValue('请检查\n  保留缩进')
    await upload(wrapper, ['需求 文档.txt', '截图.png'])
    await wrapper.find('button[title="发送"]').trigger('click')
    expect(wrapper.emitted('send')?.[0]).toEqual([`${MENTION} @${secondPath}\n请检查\n  保留缩进`])
  })

  it('旧草稿仍能恢复,重新发送时使用附件在前的顺序', async () => {
    localStorage.setItem(`agent.draft.${SID}`, `请检查\n  保留缩进\n${MENTION}`)
    const wrapper = composer()
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('请检查\n  保留缩进')
    expect(wrapper.find('.attachment-tag').text()).toBe('需求 文档.txt')
    await wrapper.find('button[title="发送"]').trigger('click')
    expect(wrapper.emitted('send')?.[0]).toEqual([`${MENTION}\n请检查\n  保留缩进`])
  })

  it('草稿保存正文和附件,重新进入会话可恢复标签并移除附件', async () => {
    const wrapper = composer()
    await wrapper.find('textarea').setValue('请检查')
    await upload(wrapper, ['需求 文档.txt'])
    expect(localStorage.getItem(`agent.draft.${SID}`)).toBe(`${MENTION}\n请检查`)
    wrapper.unmount()
    live.splice(live.indexOf(wrapper), 1)
    const restored = composer()
    expect((restored.find('textarea').element as HTMLTextAreaElement).value).toBe('请检查')
    expect(restored.find('.attachment-tag').text()).toBe('需求 文档.txt')
    await restored.find('.n-tag__close').trigger('click')
    expect(restored.find('.attachment-tag').exists()).toBe(false)
    expect(localStorage.getItem(`agent.draft.${SID}`)).toBe('请检查')
  })

  it('上传未完成时禁止发送;期间切换会话,文件仍归原会话', async () => {
    let complete!: (value: { path: string }) => void
    const secondPath = `/data/agent-files/${SID}/截图.png`
    mocks.upload.mockReturnValueOnce(new Promise((resolve) => { complete = resolve }))
      .mockResolvedValueOnce({ path: secondPath })
    const wrapper = composer()
    await wrapper.find('textarea').setValue('  检查附件')
    await upload(wrapper, ['需求 文档.txt', '截图.png'])
    expect((wrapper.find('button[title="发送"]').element as HTMLButtonElement).disabled).toBe(true)
    await wrapper.setProps({ sessionId: 's-other' })
    complete({ path: PATH })
    await flushPromises()
    expect(wrapper.find('.attachment-tag').exists()).toBe(false)
    expect(localStorage.getItem(`agent.draft.${SID}`)).toBe(`${MENTION} @${secondPath}\n  检查附件`)
    await wrapper.setProps({ sessionId: SID })
    expect(wrapper.findAll('.attachment-tag').map((tag) => tag.text())).toEqual(['需求 文档.txt', '截图.png'])
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('  检查附件')
  })

  it('多个文件中一个上传失败,保留成功附件并显示失败提示', async () => {
    mocks.upload.mockRejectedValueOnce(new Error('upload failed')).mockResolvedValueOnce({ path: PATH })
    const wrapper = composer()
    await upload(wrapper, ['失败.txt', '需求 文档.txt'])
    expect(wrapper.find('.attachment-tag').text()).toBe('需求 文档.txt')
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('(上传失败: 失败.txt)')
  })

  it('上传期间离开页面,上传完成后仍可从原会话草稿恢复附件', async () => {
    let complete!: (value: { path: string }) => void
    mocks.upload.mockReturnValue(new Promise((resolve) => { complete = resolve }))
    const wrapper = composer()
    await wrapper.find('textarea').setValue('稍后检查')
    await upload(wrapper, ['需求 文档.txt'])
    wrapper.unmount()
    live.splice(live.indexOf(wrapper), 1)
    complete({ path: PATH })
    await flushPromises()
    const restored = composer()
    expect((restored.find('textarea').element as HTMLTextAreaElement).value).toBe('稍后检查')
    expect(restored.find('.attachment-tag').text()).toBe('需求 文档.txt')
  })

  it('消息支持多个附件、旧版 @路径和带空格的文件名,保留正文与普通提及', () => {
    const legacy = `/data/agent-files/${SID}/旧版 截图.png`
    const text = `请检查 @同事 @/workspace/a.txt\n${MENTION} @${legacy}\n补充说明`
    const wrapper = mount(UserMessage, { props: { text, sessionId: SID } })
    live.push(wrapper)
    expect(wrapper.findAll('.attachment-tag').map((tag) => tag.text())).toEqual(['需求 文档.txt', '旧版 截图.png'])
    expect(wrapper.find('.user-message-body').text()).toBe('请检查 @同事 @/workspace/a.txt\n \n补充说明')
    expect(wrapper.find('.user-message').element.firstElementChild)
      .toBe(wrapper.find('.user-message-attachments').element)
    expect(wrapper.text()).not.toContain('/data/agent-files/')
    expect(wrapper.findAll('img, a')).toHaveLength(0)
  })

  it('上传的图片标签可点击预览原图,关闭后仍保留附件', async () => {
    const name = '设计 截图.PNG'
    mocks.upload.mockResolvedValue({ path: `/data/agent-files/${SID}/${name}` })
    const wrapper = composer()
    await upload(wrapper, [name])
    expect(document.body.querySelector('.attachment-preview-image')).toBeNull()
    await wrapper.find('.attachment-preview-trigger').trigger('click')
    await flushPromises()
    const image = document.body.querySelector<HTMLImageElement>('.attachment-preview-image')
    expect(image?.getAttribute('src')).toBe(`/api/agent/sessions/${SID}/files/${encodeURIComponent(name)}`)
    expect(image?.alt).toBe(name)
    document.body.querySelector<HTMLElement>('.attachment-preview .n-card-header__close')!.click()
    await flushPromises()
    expect(document.body.querySelector('.attachment-preview-image')).toBeNull()
    expect(wrapper.find('.attachment-tag').text()).toBe(name)
  })

  it('消息里的图片可预览,点击图片不触发气泡复制,普通附件仍按正文点击', async () => {
    const clicked = vi.fn()
    const name = '历史 截图.webp'
    const wrapper = mount({ components: { UserMessage },
      data: () => ({ text: `@${JSON.stringify(`/data/agent-files/${SID}/${name}`)} ${MENTION}` }),
      template: `<div @click="clicked"><UserMessage :text="text" session-id="${SID}" /></div>`,
      methods: { clicked },
    })
    live.push(wrapper)
    await wrapper.find('.attachment-tag.previewable').trigger('click')
    await flushPromises()
    expect(document.body.querySelector('.attachment-preview-image')?.getAttribute('src'))
      .toBe(`/api/agent/sessions/${SID}/files/${encodeURIComponent(name)}`)
    expect(clicked).not.toHaveBeenCalled()
    await wrapper.find('.attachment-tag:not(.previewable)').trigger('click')
    expect(clicked).toHaveBeenCalledOnce()
  })

  it('删除图片附件时不会同时打开预览', async () => {
    mocks.upload.mockResolvedValue({ path: `/data/agent-files/${SID}/截图.jpg` })
    const wrapper = composer()
    await upload(wrapper, ['截图.jpg'])
    await wrapper.find('.n-tag__close').trigger('click')
    await flushPromises()
    expect(wrapper.find('.attachment-tag').exists()).toBe(false)
    expect(document.body.querySelector('.attachment-preview-image')).toBeNull()
  })
})
