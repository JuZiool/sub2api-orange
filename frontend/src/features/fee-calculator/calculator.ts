/**
 * Orange 特有：费用计算器的纯计算函数（BigInt 十进制有理数，无浮点误差）。
 *
 * 与旧 Orange（main）实现语义完全一致，唯一差异是去掉了硬编码中文兜底文案：
 * 组名称为空时由调用方传入 `fallbackLabel`（i18n 文案），未传入时退化为 `#<序号>`。
 *
 * 计算口径：
 * - 每组「至少应充上游」= ceil(用户充值金额 × 上游倍率 ÷ 给用户倍率 × 100) 分；
 * - 「稳妥金额」= 各组中的最大值（按未舍入精确值判并列）；
 * - 「预计盈亏」= floor(用户充值金额 × 100) − 稳妥金额 分，负数表示亏损。
 */

export interface RateInput {
  name: string
  cost: string
  sell: string
}

export type RateStatus = 'blank' | 'incomplete' | 'invalid' | 'awaitingPayment' | 'ready'

export interface CalculatedRate extends RateInput {
  index: number
  label: string
  status: RateStatus
  costInvalid: boolean
  sellInvalid: boolean
  amountCents: bigint | null
}

export interface RateCalculation {
  payEntered: boolean
  payInvalid: boolean
  completeCount: number
  invalidGroups: string[]
  groups: CalculatedRate[]
  highestGroupIndexes: number[]
  highestGroupNames: string[]
  suggestedCents: bigint | null
  profitCents: bigint | null
  ready: boolean
}

/** 组名称为空时的兜底标签生成器（由视图传入 i18n 文案，避免计算层硬编码语言）。 */
export type RateLabelFallback = (index: number) => string

interface DecimalRatio {
  numerator: bigint
  denominator: bigint
}

function parseDecimal(value: string, allowZero: boolean): DecimalRatio | null {
  const input = value.trim()
  const match = /^\+?(\d*)(?:\.(\d*))?(?:e([+-]?\d+))?$/i.exec(input)
  if (!match || !(match[1] || match[2])) return null

  const fraction = match[2] || ''
  const scale = fraction.length - Number(match[3] || 0)
  if (!Number.isSafeInteger(scale) || Math.abs(scale) > 1000) return null

  const numerator = BigInt((match[1] || '0') + fraction)
  if (!allowZero && numerator === 0n) return null

  return scale < 0
    ? { numerator: numerator * 10n ** BigInt(-scale), denominator: 1n }
    : { numerator, denominator: 10n ** BigInt(scale) }
}

function ceilRatio(numerator: bigint, denominator: bigint): bigint {
  return (numerator + denominator - 1n) / denominator
}

export function formatCents(cents: bigint): string {
  const absolute = cents < 0n ? -cents : cents
  const sign = cents < 0n ? '-' : ''
  return `${sign}${absolute / 100n}.${(absolute % 100n).toString().padStart(2, '0')}`
}

export function calculateRates(
  payValue: string,
  rates: RateInput[],
  fallbackLabel?: RateLabelFallback
): RateCalculation {
  const payEntered = payValue.trim() !== ''
  const pay = payEntered ? parseDecimal(payValue, true) : null
  const groups: CalculatedRate[] = []
  const invalidGroups: string[] = []
  const highestGroupIndexes: number[] = []
  let maximum: { numerator: bigint; denominator: bigint; cents: bigint } | null = null
  let completeCount = 0

  rates.forEach((rate, index) => {
    const costEntered = rate.cost.trim() !== ''
    const sellEntered = rate.sell.trim() !== ''
    const cost = costEntered ? parseDecimal(rate.cost, true) : null
    const sell = sellEntered ? parseDecimal(rate.sell, false) : null
    const label = rate.name.trim() || fallbackLabel?.(index) || `#${index + 1}`
    const entry: CalculatedRate = {
      ...rate,
      index,
      label,
      status: 'blank',
      costInvalid: costEntered && !cost,
      sellInvalid: sellEntered && !sell,
      amountCents: null,
    }

    if (!costEntered && !sellEntered) {
      groups.push(entry)
      return
    }

    if (!costEntered || !sellEntered) {
      entry.status = 'incomplete'
      invalidGroups.push(label)
      groups.push(entry)
      return
    }

    if (!cost || !sell) {
      entry.status = 'invalid'
      invalidGroups.push(label)
      groups.push(entry)
      return
    }

    completeCount += 1
    if (!pay) {
      entry.status = 'awaitingPayment'
      groups.push(entry)
      return
    }

    const numerator = pay.numerator * cost.numerator * sell.denominator
    const denominator = pay.denominator * cost.denominator * sell.numerator
    const amountCents = ceilRatio(numerator * 100n, denominator)
    entry.amountCents = amountCents
    entry.status = 'ready'

    if (!maximum || numerator * maximum.denominator > maximum.numerator * denominator) {
      maximum = { numerator, denominator, cents: amountCents }
      highestGroupIndexes.splice(0, highestGroupIndexes.length, index)
    } else if (numerator * maximum.denominator === maximum.numerator * denominator) {
      highestGroupIndexes.push(index)
    }

    groups.push(entry)
  })

  const finalMaximum = maximum as { numerator: bigint; denominator: bigint; cents: bigint } | null
  const ready = Boolean(pay && !invalidGroups.length && finalMaximum)
  const highestGroupNames = highestGroupIndexes.map((index) => groups[index].label)
  const suggestedCents = ready && finalMaximum ? finalMaximum.cents : null
  const profitCents = ready && pay && finalMaximum
    ? (pay.numerator * 100n) / pay.denominator - finalMaximum.cents
    : null

  if (!ready) highestGroupIndexes.splice(0, highestGroupIndexes.length)

  return {
    payEntered,
    payInvalid: payEntered && !pay,
    completeCount,
    invalidGroups,
    groups,
    highestGroupIndexes,
    highestGroupNames: ready ? highestGroupNames : [],
    suggestedCents,
    profitCents,
    ready,
  }
}
