import axe from 'axe-core'
import { render } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { useState } from 'react'
import { describe, expect, it } from 'vitest'
import { PageHeader, SectionHeader } from './page-header'
import { Form, FormActions, Field } from './field'
import { Panel } from './panel'
import { EmptyState } from './empty-state'
import { SegmentedControl } from './segmented-control'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Inbox } from 'lucide-react'

function Layout() {
  const [range, setRange] = useState('day')
  return (
    <main>
      <PageHeader
        title="Missions"
        description="All your missions"
        breadcrumbs={[{ label: 'Home', href: '/' }, { label: 'Missions' }]}
      />
      <Form>
        <Field label="Name" description="Display name">
          {(props) => <Input {...props} />}
        </Field>
        <Field label="Email" error="Email is required">
          {(props) => <Input {...props} />}
        </Field>
        <FormActions>
          <Button variant="outline">Cancel</Button>
          <Button>Save</Button>
        </FormActions>
      </Form>
      <SectionHeader title="Details" />
      <Panel title="Goal">
        <p>Do the thing.</p>
      </Panel>
      <EmptyState icon={Inbox} title="No missions yet" description="Start one to see it here." />
      <SegmentedControl value={range} onChange={setRange} options={[{ value: 'day', label: 'Day' }, { value: 'week', label: 'Week' }]} aria-label="Range" />
    </main>
  )
}

describe('timothy layout accessibility', () => {
  it('has no axe violations', async () => {
    const { container } = render(
      <MemoryRouter>
        <Layout />
      </MemoryRouter>,
    )
    const results = await axe.run(container, { rules: { 'color-contrast': { enabled: false } } })
    expect(results.violations).toEqual([])
  })
})
