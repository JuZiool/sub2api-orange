import { describe, expect, it } from 'vitest'
import { calculateRates, formatCents } from '../calculator'

function rates(...values: Array<[string, string]>): Array<{ name: string; cost: string; sell: string }> {
  return Array.from({ length: 7 }, (_, index) => ({
    name: '',
    cost: values[index]?.[0] ?? '',
    sell: values[index]?.[1] ?? '',
  }))
}

describe('fee calculator', () => {
  it('rounds a repeating decimal requirement up to the next cent', () => {
    const result = calculateRates('20', rates(['0.2', '0.22']))
    expect(result.suggestedCents).toBe(1819n)
    expect(result.profitCents).toBe(181n)
    expect(formatCents(result.suggestedCents!)).toBe('18.19')
  })

  it('marks exactly tied unrounded requirements as the highest groups', () => {
    const result = calculateRates('20', rates(['0.2', '0.22'], ['0.4', '0.44']))
    expect(result.highestGroupIndexes).toEqual([0, 1])
    expect(result.highestGroupNames).toEqual(['倍率组 1', '倍率组 2'])
  })

  it('compares exact requirements when distinct values round to the same cent', () => {
    const result = calculateRates('1', rates(['1', '3'], ['1', '2.99']))
    expect(result.groups[0].amountCents).toBe(34n)
    expect(result.groups[1].amountCents).toBe(34n)
    expect(result.highestGroupIndexes).toEqual([1])
  })

  it('allows a zero upstream rate and a zero user recharge amount', () => {
    const zeroCost = calculateRates('20', rates(['0', '0.22']))
    const zeroPay = calculateRates('0', rates(['0.2', '0.22']))
    expect(zeroCost.suggestedCents).toBe(0n)
    expect(zeroPay.suggestedCents).toBe(0n)
    expect(zeroPay.profitCents).toBe(0n)
  })

  it('shows a loss when the recharge amount floors below the required upstream amount', () => {
    const result = calculateRates('0.005', rates(['1', '1']))
    expect(result.suggestedCents).toBe(1n)
    expect(result.profitCents).toBe(-1n)
    expect(formatCents(result.profitCents!)).toBe('-0.01')
  })

  it('blocks the summary when any partially entered group is invalid', () => {
    const result = calculateRates('20', rates(['0.2', '0.22'], ['0.3', '']))
    expect(result.ready).toBe(false)
    expect(result.suggestedCents).toBeNull()
    expect(result.groups[0].amountCents).toBe(1819n)
    expect(result.invalidGroups).toEqual(['倍率组 2'])
  })

  it('rejects a zero user rate and invalid decimal values', () => {
    const zeroSell = calculateRates('20', rates(['0.2', '0']))
    const invalid = calculateRates('20', rates(['1e1001', '1']))
    expect(zeroSell.ready).toBe(false)
    expect(zeroSell.groups[0].sellInvalid).toBe(true)
    expect(invalid.ready).toBe(false)
    expect(invalid.groups[0].costInvalid).toBe(true)
  })

  it('accepts scientific notation and distinguishes blank pay from zero', () => {
    const blankPay = calculateRates('', rates(['0.2', '0.22']))
    const scientific = calculateRates('2e1', rates(['2e-1', '2.2e-1']))
    expect(blankPay.payEntered).toBe(false)
    expect(blankPay.groups[0].status).toBe('awaitingPayment')
    expect(scientific.suggestedCents).toBe(1819n)
  })
})
