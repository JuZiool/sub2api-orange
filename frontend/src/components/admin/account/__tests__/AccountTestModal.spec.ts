import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountTestModal from '../AccountTestModal.vue'

const { getAvailableModels, copyToClipboard } = vi.hoisted(() => ({
  getAvailableModels: vi.fn(),
  copyToClipboard: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getAvailableModels
    }
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'admin.accounts.imagePromptDefault': 'Generate a cute orange cat astronaut sticker on a clean pastel background.'
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.accounts.imageReceived' && params?.count) {
          return `received-${params.count}`
        }
        if (key === 'admin.accounts.imagePreviewAlt' && params?.index) {
          return `test-image-${params.index}`
        }
        return messages[key] || key
      }
    })
  }
})

function createStreamResponse(lines: string[]) {
  const encoder = new TextEncoder()
  const chunks = lines.map((line) => encoder.encode(line))
  let index = 0

  return {
    ok: true,
    body: {
      getReader: () => ({
        read: vi.fn().mockImplementation(async () => {
          if (index < chunks.length) {
            return { done: false, value: chunks[index++] }
          }
          return { done: true, value: undefined }
        })
      })
    }
  } as Response
}

function mountModal(account: Record<string, unknown> = {
  id: 42,
  name: 'Gemini Image Test',
  platform: 'gemini',
  type: 'apikey',
  status: 'active'
}) {
  return mount(AccountTestModal, {
    props: {
      show: false,
      account
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Select: { template: '<div class="select-stub"></div>' },
        TextArea: {
          props: ['modelValue'],
          emits: ['update:modelValue'],
          template: '<textarea class="textarea-stub" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
        },
        Icon: true
      }
    }
  })
}

describe('AccountTestModal', () => {
  beforeEach(() => {
    getAvailableModels.mockResolvedValue([
      { id: 'gemini-2.0-flash', display_name: 'Gemini 2.0 Flash' },
      { id: 'gemini-2.5-flash-image', display_name: 'Gemini 2.5 Flash Image' },
      { id: 'gemini-3.1-flash-image', display_name: 'Gemini 3.1 Flash Image' }
    ])
    copyToClipboard.mockReset()
    Object.defineProperty(globalThis, 'localStorage', {
      value: {
        getItem: vi.fn((key: string) => (key === 'auth_token' ? 'test-token' : null)),
        setItem: vi.fn(),
        removeItem: vi.fn(),
        clear: vi.fn()
      },
      configurable: true
    })
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"gemini-2.5-flash-image"}\n',
        'data: {"type":"image","image_url":"data:image/png;base64,QUJD","mime_type":"image/png"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it.each([
    ['openai', 'gpt-5.4'],
    ['anthropic', 'claude-sonnet-4-6'],
    ['gemini', 'gemini-2.5-flash'],
    ['grok', 'grok-4.3']
  ])('%s 普通文本测试会发送自定义提示词原文', async (platform, model) => {
    getAvailableModels.mockResolvedValue([{ id: model, display_name: model }])
    const wrapper = mountModal({ id: 42, name: 'Text Test', platform, type: 'oauth', status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const prompt = '  请原样回复你好🙂\n'
    await wrapper.find('textarea.textarea-stub').setValue(prompt)
    expect(wrapper.text()).toContain('admin.accounts.textTestPromptCustomSummary')
    await (wrapper.vm as any).startTest()
    await flushPromises()

    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toMatchObject({ model_id: model, prompt })
  })

  it('文本提示词留空使用默认值，重新打开弹窗清空自定义文本', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'gpt-5.4', display_name: 'GPT-5.4' }])
    const wrapper = mountModal({ id: 42, name: 'Text Test', platform: 'openai', type: 'oauth', status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.textTestPromptDefaultSummary')
    await (wrapper.vm as any).startTest()
    await flushPromises()
    expect(JSON.parse((global.fetch as any).mock.calls[0][1].body).prompt).toBe('')

    await wrapper.find('textarea.textarea-stub').setValue('temporary question')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('')
  })

  it('文本提示词按 Unicode 字符限制长度，超长时禁止测试', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'gpt-5.4', display_name: 'GPT-5.4' }])
    const wrapper = mountModal({ id: 42, name: 'Text Test', platform: 'openai', type: 'oauth', status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()
    const textarea = wrapper.find('textarea.textarea-stub')
    await textarea.setValue('🙂'.repeat(8192))
    expect((wrapper.vm as any).canStartTest).toBe(true)
    await textarea.setValue('🙂'.repeat(8193))
    expect((wrapper.vm as any).canStartTest).toBe(false)
    await (wrapper.vm as any).startTest()
    expect(global.fetch).not.toHaveBeenCalled()
  })

  it('文本和图片模式分别保留提示词，图片请求不使用文本提示词', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.find('textarea').setValue('draw a cat')
    ;(wrapper.vm as any).selectedModelId = 'gemini-2.0-flash'
    await flushPromises()
    await wrapper.find('textarea').setValue('describe a cat')
    ;(wrapper.vm as any).selectedModelId = 'gemini-3.1-flash-image'
    await flushPromises()
    expect((wrapper.find('textarea').element as HTMLTextAreaElement).value).toBe('draw a cat')
    await (wrapper.vm as any).startTest()
    await flushPromises()
    expect(JSON.parse((global.fetch as any).mock.calls[0][1].body).prompt).toBe('draw a cat')
  })

  it('gemini 图片模型测试会携带提示词并渲染图片预览', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const promptInput = wrapper.find('textarea.textarea-stub')
    expect(promptInput.exists()).toBe(true)
    await promptInput.setValue('draw a tiny orange cat astronaut')

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'gemini-3.1-flash-image',
      prompt: 'draw a tiny orange cat astronaut'
    })

    const preview = wrapper.find('img[alt="test-image-1"]')
    expect(preview.exists()).toBe(true)
    expect(preview.attributes('src')).toBe('data:image/png;base64,QUJD')
  })

  it('grok 账号测试默认选择 Grok 模型', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'grok-4.3', display_name: 'Grok 4.3' },
      { id: 'grok-build-0.1', display_name: 'Grok Build 0.1' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"grok-4.3"}\n',
        'data: {"type":"content","text":"ok"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 13,
      name: 'Grok Account',
      platform: 'grok',
      type: 'oauth',
      status: 'active'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'grok-4.3',
      prompt: '',
      mode: 'text'
    })
  })

  it('OpenAI Compact 探测会携带 compact 测试模式', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-5.4', display_name: 'GPT-5.4' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 42,
      name: 'OpenAI OAuth',
      platform: 'openai',
      type: 'oauth',
      status: 'active'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    await wrapper.find('textarea').setValue('custom question')
    ;(wrapper.vm as any).selectedModelId = 'gpt-5.4'
    ;(wrapper.vm as any).testMode = 'compact'
    await flushPromises()
    expect(wrapper.find('textarea').exists()).toBe(false)
    await (wrapper.vm as any).startTest()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toMatchObject({
      model_id: 'gpt-5.4',
      prompt: '',
      mode: 'compact'
    })
  })
})
