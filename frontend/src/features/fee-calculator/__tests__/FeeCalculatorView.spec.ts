import { flushPromises, mount, type DOMWrapper, type VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import common from '@/i18n/locales/zh/common'
import { useAuthStore } from '@/stores/auth'
import FeeCalculatorView from '../FeeCalculatorView.vue'

const { getAll, authState } = vi.hoisted(() => ({
  getAll: vi.fn(),
  authState: { isSimpleMode: false },
}))

vi.mock('@/api/admin/groups', () => ({ getAll, default: { getAll }, groupsAPI: { getAll } }))
vi.mock('@/stores/auth', async () => {
  const { reactive } = await import('vue')
  const auth = reactive(authState)
  return { useAuthStore: () => auth }
})

const STORAGE_KEY = 'rateCalc.shared.v2'
const mounted: VueWrapper[] = []

function group(id: number, name: string, cost = 0.2, sell = 0.25) {
  return { id, name, status: 'active', upstream_rate_multiplier: cost, rate_multiplier: sell }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

function render() {
  const wrapper = mount(FeeCalculatorView, {
    global: {
      plugins: [createI18n({ legacy: false, locale: 'zh', messages: { zh: common } })],
      stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true },
    },
  })
  mounted.push(wrapper)
  return wrapper
}

function cards(wrapper: VueWrapper) {
  return wrapper.findAll('article')
}

function field(card: DOMWrapper<Element>, prefix: string) {
  return card.get<HTMLInputElement>(`input[id^="rate-${prefix}-"]`)
}

function names(wrapper: VueWrapper) {
  return cards(wrapper).map((card) => field(card, 'name').element.value)
}

function button(wrapper: VueWrapper, text: string) {
  const found = wrapper.findAll('button').find((item) => item.text().includes(text))
  expect(found, `button containing ${text}`).toBeDefined()
  return found!
}

function summaryAmount(wrapper: VueWrapper) {
  return wrapper.findAll('.text-3xl')[0].text()
}

function translation(key: string) {
  return (common.rateCalculator as Record<string, string>)[key]
}

function save(rates: unknown[], pay = '20') {
  localStorage.setItem(STORAGE_KEY, JSON.stringify({ on: true, pay, rates }))
}

describe('FeeCalculatorView automatic group synchronization', () => {
  beforeEach(() => {
    getAll.mockReset()
    getAll.mockResolvedValue([])
    authState.isSimpleMode = false
    localStorage.clear()
  })

  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.restoreAllMocks()
    localStorage.clear()
  })

  it('imports all groups automatically, keeps zero cost, and makes synchronized fields readonly', async () => {
    getAll.mockResolvedValue([group(10, 'Claude'), group(20, '免费组', 0, 1)])
    const wrapper = render()
    await flushPromises()

    expect(getAll).toHaveBeenCalledTimes(1)
    expect(names(wrapper)).toEqual(['Claude', '免费组'])
    expect(field(cards(wrapper)[0], 'cost').element.value).toBe('0.2')
    expect(field(cards(wrapper)[0], 'sell').element.value).toBe('0.25')
    expect(field(cards(wrapper)[1], 'cost').element.value).toBe('0')
    cards(wrapper).forEach((card) => {
      for (const prefix of ['name', 'cost', 'sell']) {
        expect(field(card, prefix).element.readOnly).toBe(true)
      }
      expect(card.text()).toContain('自动同步')
      expect(card.findAll('button')).toHaveLength(0)
    })
    await wrapper.get('#rate-calculator-pay').setValue('20')
    expect(summaryAmount(wrapper)).toContain('16.00')
  })

  it('updates by group ID, appends new groups, and preserves manual entries on focus refresh', async () => {
    getAll.mockResolvedValue([group(10, '旧名称')])
    const wrapper = render()
    await flushPromises()
    await wrapper.get('#rate-calculator-pay').setValue('30')
    await button(wrapper, '添加手动分组').trigger('click')
    const manual = cards(wrapper)[1]
    await field(manual, 'name').setValue('手动预算')
    await field(manual, 'cost').setValue('0.3')
    await field(manual, 'sell').setValue('0.5')

    getAll.mockResolvedValue([group(10, '新名称', 0.4, 0.5), group(30, '新分组')])
    window.dispatchEvent(new Event('focus'))
    await flushPromises()

    expect(getAll).toHaveBeenCalledTimes(2)
    expect(names(wrapper)).toEqual(['新名称', '新分组', '手动预算'])
    expect(field(cards(wrapper)[0], 'cost').element.value).toBe('0.4')
    expect(field(cards(wrapper)[2], 'cost').element.value).toBe('0.3')
    expect(field(cards(wrapper)[2], 'sell').element.value).toBe('0.5')
    expect(wrapper.get<HTMLInputElement>('#rate-calculator-pay').element.value).toBe('30')

    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(names(wrapper)).toEqual(['新名称', '新分组', '手动预算'])
  })

  it('restores legacy manual memory and persists group IDs without duplicating imported cards on remount', async () => {
    save([{ name: '原手填组', cost: '0.3', sell: '0.5' }])
    getAll.mockResolvedValue([group(10, '服务器组')])
    const wrapper = render()
    await flushPromises()

    expect(names(wrapper)).toEqual(['服务器组', '原手填组'])
    expect(wrapper.get<HTMLInputElement>('#rate-calculator-pay').element.value).toBe('20')
    const remembered = JSON.parse(localStorage.getItem(STORAGE_KEY)!)
    expect(remembered.rates).toContainEqual({ groupId: 10, name: '服务器组', cost: '0.2', sell: '0.25' })
    expect(remembered.rates).toContainEqual({ name: '原手填组', cost: '0.3', sell: '0.5' })
    wrapper.unmount()
    mounted.splice(mounted.indexOf(wrapper), 1)

    const restored = render()
    await flushPromises()
    expect(names(restored)).toEqual(['服务器组', '原手填组'])
  })

  it('blocks cached automatic rates during loading and failure, and permits a successful retry', async () => {
    save([{ groupId: 10, name: '旧缓存', cost: '0.9', sell: '1' }])
    const pending = deferred<ReturnType<typeof group>[]>()
    getAll.mockReturnValueOnce(pending.promise)
    const wrapper = render()
    await flushPromises()

    expect(summaryAmount(wrapper)).toBe('—')
    expect(wrapper.text()).toContain(translation('syncLoading'))
    expect(button(wrapper, '刷新分组倍率').attributes('disabled')).toBeDefined()
    pending.reject(new Error('offline'))
    await flushPromises()
    expect(summaryAmount(wrapper)).toBe('—')
    expect(wrapper.text()).toContain(translation('syncError'))

    getAll.mockResolvedValue([group(10, '最新配置')])
    await button(wrapper, '刷新分组倍率').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toEqual(['最新配置'])
    expect(summaryAmount(wrapper)).toContain('16.00')
  })

  it('blocks the summary while a later refresh is pending or fails', async () => {
    getAll.mockResolvedValue([group(10, '服务器组')])
    const wrapper = render()
    await flushPromises()
    await wrapper.get('#rate-calculator-pay').setValue('20')
    expect(summaryAmount(wrapper)).toContain('16.00')

    const pending = deferred<ReturnType<typeof group>[]>()
    getAll.mockReturnValueOnce(pending.promise)
    window.dispatchEvent(new Event('focus'))
    await flushPromises()
    expect(summaryAmount(wrapper)).toBe('—')
    pending.reject(new Error('offline'))
    await flushPromises()
    expect(summaryAmount(wrapper)).toBe('—')
    expect(wrapper.text()).toContain(translation('syncError'))
  })

  it.each([
    { id: 10, name: '缺成本', rate_multiplier: 1 },
    { id: 10, name: '缺售价', upstream_rate_multiplier: 0.2 },
    { id: 0, name: '无效ID', upstream_rate_multiplier: 0.2, rate_multiplier: 1 },
    { id: 10, name: '无穷值', upstream_rate_multiplier: Infinity, rate_multiplier: 1 },
  ])('rejects malformed group responses instead of assuming rates: $name', async (malformed) => {
    getAll.mockResolvedValue([group(20, '有效组'), malformed])
    const wrapper = render()
    await flushPromises()
    await wrapper.get('#rate-calculator-pay').setValue('20')
    expect(wrapper.text()).toContain(translation('syncError'))
    expect(summaryAmount(wrapper)).toBe('—')
  })

  it('keeps invalid configured rates visible and blocks the calculator instead of skipping the group', async () => {
    getAll.mockResolvedValue([group(10, '有效组'), group(20, '零售价', 0.2, 0)])
    const wrapper = render()
    await flushPromises()
    await wrapper.get('#rate-calculator-pay').setValue('20')
    expect(names(wrapper)).toEqual(['有效组', '零售价'])
    expect(field(cards(wrapper)[1], 'sell').attributes('aria-invalid')).toBe('true')
    expect(summaryAmount(wrapper)).toBe('—')
    expect(wrapper.text()).toContain('零售价 尚未填写完整或数值无效')
  })

  it('removes groups missing from the latest result and preserves explicitly added blank cards', async () => {
    getAll.mockResolvedValue([group(10, '旧组'), group(20, '保留组')])
    const wrapper = render()
    await flushPromises()
    await button(wrapper, '添加手动分组').trigger('click')

    getAll.mockResolvedValue([group(20, '保留组')])
    await button(wrapper, '刷新分组倍率').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toEqual(['保留组', ''])
    expect(field(cards(wrapper)[1], 'name').element.readOnly).toBe(false)

    getAll.mockResolvedValue([])
    await button(wrapper, '刷新分组倍率').trigger('click')
    await flushPromises()
    expect(names(wrapper)).toEqual([''])
  })

  it('coalesces focus and visibility requests and only refreshes when visible', async () => {
    const initial = deferred<ReturnType<typeof group>[]>()
    getAll.mockReturnValueOnce(initial.promise)
    const wrapper = render()
    const visibility = vi.spyOn(document, 'visibilityState', 'get')
    visibility.mockReturnValue('visible')
    window.dispatchEvent(new Event('focus'))
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(getAll).toHaveBeenCalledTimes(1)
    initial.resolve([group(10, '已同步')])
    await flushPromises()
    getAll.mockResolvedValue([group(10, '已同步')])

    visibility.mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(getAll).toHaveBeenCalledTimes(1)
    visibility.mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(getAll).toHaveBeenCalledTimes(2)
    expect(names(wrapper)).toEqual(['已同步'])
  })

  it('removes refresh listeners and does not write a late response after unmount', async () => {
    save([{ groupId: 10, name: '旧缓存', cost: '0.9', sell: '1' }])
    const pending = deferred<ReturnType<typeof group>[]>()
    getAll.mockReturnValueOnce(pending.promise)
    const wrapper = render()
    await flushPromises()
    wrapper.unmount()
    mounted.splice(mounted.indexOf(wrapper), 1)
    const savedBeforeResponse = localStorage.getItem(STORAGE_KEY)

    pending.resolve([group(10, '晚到的新配置')])
    await flushPromises()
    expect(localStorage.getItem(STORAGE_KEY)).toBe(savedBeforeResponse)
    window.dispatchEvent(new Event('focus'))
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(getAll).toHaveBeenCalledTimes(1)
  })

  it('keeps manual calculation available in simple mode and discards cached automatic rates', async () => {
    authState.isSimpleMode = true
    save([
      { groupId: 10, name: '不能使用的旧配置', cost: '9', sell: '1' },
      { name: '手动组', cost: '0.2', sell: '0.25' },
    ])
    const wrapper = render()
    await flushPromises()
    expect(getAll).not.toHaveBeenCalled()
    expect(names(wrapper).filter(Boolean)).toEqual(['手动组'])
    expect(summaryAmount(wrapper)).toContain('16.00')
    expect(wrapper.text()).toContain('简易模式')
    expect(field(cards(wrapper)[0], 'cost').element.readOnly).toBe(false)
  })

  it('ignores an outstanding response when auth switches to simple mode and synchronizes when standard mode returns', async () => {
    save([
      { groupId: 10, name: '旧缓存', cost: '9', sell: '1' },
      { name: '手动组', cost: '0.2', sell: '0.25' },
    ])
    const pending = deferred<ReturnType<typeof group>[]>()
    getAll.mockReturnValueOnce(pending.promise)
    const wrapper = render()
    await flushPromises()
    expect(summaryAmount(wrapper)).toBe('—')

    const auth = useAuthStore()
    auth.isSimpleMode = true
    await flushPromises()
    expect(names(wrapper).filter(Boolean)).toEqual(['手动组'])
    expect(summaryAmount(wrapper)).toContain('16.00')

    pending.resolve([group(10, '不应使用的晚到响应', 9, 1)])
    await flushPromises()
    expect(names(wrapper).filter(Boolean)).toEqual(['手动组'])
    expect(summaryAmount(wrapper)).toContain('16.00')

    getAll.mockResolvedValue([group(20, '新标准模式组', 0.3, 0.25)])
    auth.isSimpleMode = false
    await flushPromises()
    expect(getAll).toHaveBeenCalledTimes(2)
    expect(names(wrapper)).toEqual(['新标准模式组', '手动组'])
    expect(summaryAmount(wrapper)).toContain('24.00')
  })

  it('resets manual inputs and saved memory while retaining automatic group configuration', async () => {
    save([{ name: '手动组', cost: '0.3', sell: '0.5' }])
    getAll.mockResolvedValue([group(10, '服务器组')])
    const wrapper = render()
    await flushPromises()
    await button(wrapper, '全部重置').trigger('click')
    await flushPromises()

    expect(names(wrapper)).toEqual(['服务器组', ''])
    expect(field(cards(wrapper)[0], 'cost').element.value).toBe('0.2')
    expect(field(cards(wrapper)[0], 'sell').element.value).toBe('0.25')
    expect(field(cards(wrapper)[1], 'cost').element.value).toBe('')
    expect(field(cards(wrapper)[1], 'sell').element.value).toBe('')
    expect(wrapper.get<HTMLInputElement>('#rate-calculator-pay').element.value).toBe('')
    expect(wrapper.get<HTMLInputElement>('input[type="checkbox"]').element.checked).toBe(false)
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
  })
})
