import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { StatsLegend } from './StatsLegend'
import { compact } from '../../lib/format'

afterEach(cleanup)

const rows = [
  { bucket: 'a', openai: 2, anthropic: 10 },
  { bucket: 'b', openai: 4, anthropic: 0 },
  { bucket: 'c', openai: 9, anthropic: 5 },
]
const groups = ['openai', 'anthropic']
const colorOf = (g: string) => (g === 'openai' ? '#111' : '#222')
const valueLabel = (v: number) => v.toFixed(1)

describe('StatsLegend', () => {
  it('renders nothing when there are no groups', () => {
    const { container } = render(
      <StatsLegend rows={[]} groups={[]} colorOf={colorOf} hidden={new Set()} onSelect={vi.fn()} valueLabel={valueLabel} />,
    )
    expect(container.firstChild).toBeNull()
  })

  it('computes mean/last/max per group', () => {
    render(
      <StatsLegend rows={rows} groups={groups} colorOf={colorOf} hidden={new Set()} onSelect={vi.fn()} valueLabel={valueLabel} />,
    )
    // openai: mean (2+4+9)/3 = 5, last 9, max 9
    const row = screen.getByText('openai').closest('tr')
    expect(row).not.toBeNull()
    expect(row).toHaveTextContent('5.0')
    expect(row).toHaveTextContent('9.0')
  })

  it('clicking a name calls onSelect non-additively and shows strikethrough when hidden', () => {
    const onSelect = vi.fn()
    const { rerender } = render(
      <StatsLegend rows={rows} groups={groups} colorOf={colorOf} hidden={new Set()} onSelect={onSelect} valueLabel={valueLabel} />,
    )
    const name = screen.getByText('openai')
    expect(name).not.toHaveStyle({ textDecoration: 'line-through' })
    fireEvent.click(name)
    expect(onSelect).toHaveBeenCalledWith('openai', false)

    rerender(
      <StatsLegend rows={rows} groups={groups} colorOf={colorOf} hidden={new Set(['openai'])} onSelect={onSelect} valueLabel={valueLabel} />,
    )
    expect(screen.getByText('openai')).toHaveStyle({ textDecoration: 'line-through' })
  })

  it('ctrl/cmd-click calls onSelect additively', () => {
    const onSelect = vi.fn()
    render(
      <StatsLegend rows={rows} groups={groups} colorOf={colorOf} hidden={new Set()} onSelect={onSelect} valueLabel={valueLabel} />,
    )
    fireEvent.click(screen.getByText('openai'), { ctrlKey: true })
    expect(onSelect).toHaveBeenCalledWith('openai', true)
    fireEvent.click(screen.getByText('anthropic'), { metaKey: true })
    expect(onSelect).toHaveBeenCalledWith('anthropic', true)
  })

  it('supports keyboard activation (Enter/Space) as non-additive selection', () => {
    const onSelect = vi.fn()
    render(
      <StatsLegend rows={rows} groups={groups} colorOf={colorOf} hidden={new Set()} onSelect={onSelect} valueLabel={valueLabel} />,
    )
    const button = screen.getByText('anthropic').closest('[role="button"]')
    if (!button) throw new Error('button not found')
    fireEvent.keyDown(button, { key: 'Enter' })
    fireEvent.keyDown(button, { key: ' ' })
    expect(onSelect).toHaveBeenNthCalledWith(1, 'anthropic', false)
    expect(onSelect).toHaveBeenNthCalledWith(2, 'anthropic', false)
  })

  it('a group missing from a row treats it as zero', () => {
    render(
      <StatsLegend rows={rows} groups={groups} colorOf={colorOf} hidden={new Set()} onSelect={vi.fn()} valueLabel={valueLabel} />,
    )
    // anthropic: (10+0+5)/3 = 5, last 5, max 10
    const row = screen.getByText('anthropic').closest('tr')
    expect(row).toHaveTextContent('10.0')
  })

  it('renders a fractional mean rounded, never at raw float precision, when fed the compact formatter', () => {
    // openai: (2+3+14)/3 = 6.333... — a repeating decimal, the exact
    // shape of the bug (long unrounded float rendered next to "k"-
    // suffixed values elsewhere in the panel).
    const fractionalRows = [
      { bucket: 'a', openai: 2 },
      { bucket: 'b', openai: 3 },
      { bucket: 'c', openai: 14 },
    ]
    render(
      <StatsLegend
        rows={fractionalRows}
        groups={['openai']}
        colorOf={colorOf}
        hidden={new Set()}
        onSelect={vi.fn()}
        valueLabel={compact}
      />,
    )
    const row = screen.getByText('openai').closest('tr')
    expect(row).toHaveTextContent('6.33')
    expect(row).not.toHaveTextContent('6.333333333333333')
  })
})
