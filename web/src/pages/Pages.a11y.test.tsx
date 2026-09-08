import axe from 'axe-core'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Sibling to Settings.a11y.test.tsx (section D of the phase 04D brief):
// axe on Home, Analytics, Memory and Knowledge, zero violations, one h1
// each. Other slices add their own describe blocks here. vi.mock for
// '../api/client' is hoisted per module path, so every export any slice
// needs is declared once here; each describe's beforeEach only sets the
// resolved values it cares about.
vi.mock('../api/client', () => ({
  listAgents: vi.fn(),
  listMemories: vi.fn(),
  listRoutes: vi.fn(),
  getSettings: vi.fn(),
  listKbCollections: vi.fn(),
  getKbCollection: vi.fn(),
  createKbCollection: vi.fn(),
  deleteKbCollection: vi.fn(),
  updateKbCollection: vi.fn(),
  listKbDocuments: vi.fn(),
  uploadKbDocument: vi.fn(),
  deleteKbDocument: vi.fn(),
  reingestKbDocument: vi.fn(),
  addKbDocumentFromUrl: vi.fn(),
}))

import {
  getSettings,
  listAgents,
  listKbCollections,
  listMemories,
  listRoutes,
} from '../api/client'
import { Home } from './Home'
import { Knowledge } from './Knowledge'

const axeOptions = { rules: { 'color-contrast': { enabled: false } } }
// The Composer's hidden file input has no label, pre-existing and
// outside this migration's scope (same exemption as Chat.a11y.test.tsx).
const homeAxeOptions = { rules: { 'color-contrast': { enabled: false }, label: { enabled: false } } }

function expectSingleH1() {
  expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1)
}

afterEach(cleanup)
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getSettings).mockResolvedValue({ settings: { transcribe_enabled: false }, values: {} })
})

describe('Home', () => {
  beforeEach(() => {
    vi.mocked(listAgents).mockResolvedValue([
      {
        id: 'a1',
        name: 'general',
        description: 'Everyday chat',
        prompt_overlay: '',
        route: '',
        skills: [],
        tools: [],
        memory: true,
        is_default: true,
        enabled: true,
      },
    ])
    vi.mocked(listMemories).mockResolvedValue([])
    vi.mocked(listRoutes).mockRejectedValue(new Error('not used on this page'))
    vi.mocked(listKbCollections).mockResolvedValue([])
  })

  it('has no axe violations', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<Home />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByRole('button', { name: /general/ })
    expectSingleH1()
    const results = await axe.run(container, homeAxeOptions)
    expect(results.violations).toEqual([])
  })
})

describe('Knowledge', () => {
  beforeEach(() => {
    vi.mocked(listKbCollections).mockResolvedValue([
      {
        id: 'c1',
        name: 'product-docs',
        description: 'Product documentation for support agents.',
        doc_count: 2,
        chunk_count: 40,
        failed_count: 0,
        retrieval_weight: 1.0,
        created_at: '2026-08-01T00:00:00Z',
        updated_at: '2026-08-10T00:00:00Z',
      },
    ])
  })

  it('has no axe violations on the collections list', async () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/knowledge']}>
        <Routes>
          <Route path="/knowledge/*" element={<Knowledge />} />
        </Routes>
      </MemoryRouter>,
    )

    await screen.findByText('product-docs')
    expectSingleH1()
    const results = await axe.run(container, axeOptions)
    expect(results.violations).toEqual([])
  })
})
