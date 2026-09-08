import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { BrandTile } from './brand-tile'

describe('BrandTile', () => {
  it('renders a sprite use element for spriteId', () => {
    const { container } = render(<BrandTile spriteId="plogo-openai" label="OpenAI" />)
    const use = container.querySelector('use')
    expect(use?.getAttribute('href')).toBe('#plogo-openai')
  })

  it('renders an image with empty alt for image', () => {
    render(<BrandTile image="/logo.png" label="Gmail" />)
    const img = screen.getByRole('img', { name: 'Gmail' }).querySelector('img')
    expect(img?.getAttribute('alt')).toBe('')
  })

  it('is role img with the label when a label is given', () => {
    render(<BrandTile spriteId="plogo-openai" label="OpenAI" />)
    expect(screen.getByRole('img', { name: 'OpenAI' })).toBeInTheDocument()
  })

  it('is aria-hidden when no label is given', () => {
    const { container } = render(<BrandTile spriteId="plogo-openai" />)
    expect(container.querySelector('[aria-hidden="true"]')).toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  it('renders the dashed fallback when nothing is given', () => {
    render(<BrandTile />)
    expect(screen.getByText('⌁')).toBeInTheDocument()
  })
})
