import { mkdirSync, writeFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'

// Onboarding canary: a fresh install walked like a new operator, from
// the installer's token link to a finished sample mission. Run by
// scripts/canary-onboarding.sh against a throwaway compose project.

const token = process.env.TIMOTHY_API_TOKEN ?? ''
const presetId = process.env.CANARY_PROVIDER_PRESET ?? 'openai'
const providerKey = process.env.CANARY_PROVIDER_KEY ?? ''
const providerBaseURL = process.env.CANARY_PROVIDER_BASE_URL ?? ''
const model = process.env.CANARY_MODEL ?? ''
const runTag = process.env.CANARY_RUN_TAG ?? 'onboarding-local'

// Tile titles on the wizard's source step (SourceStep.tsx).
const tileTitles: Record<string, string> = {
  ollama: 'Local model (Ollama)',
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  glm: 'GLM (Z.ai)',
  grok: 'Grok (xAI)',
}

const stageNames = [
  'token',
  'wizard_source',
  'provider_verified',
  'roles_shown',
  'wizard_done',
  'first_reply',
  'sample_mission_created',
  'sample_mission_terminal',
  'checklist_ticks',
] as const
type StageName = (typeof stageNames)[number]

interface StageResult {
  name: StageName
  status: 'ok' | 'fail' | 'skipped'
  ms: number
  detail?: string
}

const reportDir = 'e2e/.out'
const reportPath = `${reportDir}/onboarding-${runTag}.json`
const results: StageResult[] = stageNames.map((name) => ({ name, status: 'skipped', ms: 0 }))

function writeReport() {
  mkdirSync(reportDir, { recursive: true })
  writeFileSync(reportPath, JSON.stringify({ tag: runTag, stages: results }, null, 2))
}

// scrub keeps the API token and the provider key out of anything this
// spec prints or writes.
function scrub(text: string): string {
  let out = text
  for (const secret of [token, providerKey]) {
    if (secret) out = out.split(secret).join('[redacted]')
  }
  return out
}

// stage records one step's outcome in the report. A failure rethrows,
// so every later stage stays recorded as skipped.
async function stage(name: StageName, fn: () => Promise<void>) {
  const r = results.find((x) => x.name === name)!
  const start = Date.now()
  try {
    await fn()
    r.status = 'ok'
  } catch (err) {
    r.status = 'fail'
    // Strips terminal colour codes from Playwright's messages.
    // eslint-disable-next-line no-control-regex
    r.detail = scrub(err instanceof Error ? err.message : String(err)).replace(/\u001b\[\d+m/g, '').slice(0, 2000)
    throw new Error(`stage ${name} failed: ${r.detail}`)
  } finally {
    r.ms = Date.now() - start
    writeReport()
  }
}

const auth = () => ({ Authorization: `Bearer ${token}` })

async function missionState(page: Page, id: string) {
  const res = await page.request.get(`/v1/missions/${id}`, { headers: auth() })
  if (!res.ok()) throw new Error(`GET mission ${id}: http ${res.status()}`)
  return (await res.json()) as { phase: string; status: string }
}

async function lastEvents(page: Page, id: string): Promise<string> {
  const res = await page.request.get(`/v1/missions/${id}/events`, { headers: auth() })
  if (!res.ok()) return `events unavailable (http ${res.status()})`
  const { events } = (await res.json()) as { events: { kind: string; payload?: unknown }[] }
  return events
    .slice(-15)
    .map((e) => `${e.kind} ${JSON.stringify(e.payload ?? {}).slice(0, 200)}`)
    .join('\n')
}

test('fresh install reaches a finished sample mission', async ({ page }) => {
  writeReport()
  let missionId = ''

  await stage('token', async () => {
    if (!token) throw new Error('TIMOTHY_API_TOKEN is not set')
    // A first plain load proves the web app is reachable, so a failed
    // navigation never prints the token link.
    await page.goto('/health')
    await page.goto(`/#token=${token}`)
    await expect(page).toHaveURL(/\/welcome$/, { timeout: 30_000 })
    await expect(page.getByRole('dialog', { name: 'Settings' })).toHaveCount(0)
  })

  await stage('wizard_source', async () => {
    const title = tileTitles[presetId]
    if (!title) throw new Error(`unsupported CANARY_PROVIDER_PRESET ${presetId}`)
    await page.getByRole('button', { name: "Let's start" }).click()
    await page
      .getByRole('button')
      .filter({ has: page.getByText(title, { exact: true }) })
      .click()
    await expect(page.getByRole('heading', { name: /^Connect / })).toBeVisible()
  })

  await stage('provider_verified', async () => {
    if (providerKey) await page.getByLabel('API key', { exact: true }).fill(providerKey)
    if (providerBaseURL) {
      await page.getByText('Advanced: base URL').click()
      await page.getByLabel('Base URL', { exact: true }).fill(providerBaseURL)
    }
    if (model) {
      await page.getByLabel('Model', { exact: true }).fill(model)
      await page.keyboard.press('Escape')
    }
    const add = page.getByRole('button', { name: 'Add provider' })
    const passed = page.getByText(/^OK, .+ answered in \d+ ms\./)
    const failed = page.getByText(/^Failed after \d+ ms/)
    await page.getByRole('button', { name: 'Test connection' }).click()
    await expect(passed.or(failed)).toBeVisible({ timeout: 120_000 })
    // A cold local model can miss the first test's deadline while it
    // loads; an operator would press Test again, so the canary does once.
    if (await failed.isVisible()) {
      await page.getByRole('button', { name: 'Test connection' }).click()
      await expect(add).toBeEnabled({ timeout: 120_000 })
    }
    await add.click()
    await expect(page.getByText(/^Connected, \d+ ms$/)).toBeVisible({ timeout: 120_000 })
    await expect(page.getByRole('heading', { name: 'What Timothy uses' })).toBeVisible({ timeout: 10_000 })
  })

  await stage('roles_shown', async () => {
    const lines = [/^(Chat answers with |Chat: no model yet)/, /^Summaries/, /^Memory and knowledge search/, /^Images/]
    for (const line of lines) await expect(page.getByText(line)).toBeVisible({ timeout: 30_000 })
    await expect(page.getByText(/^Chat answers with /)).toBeVisible()
  })

  await stage('wizard_done', async () => {
    await page.getByRole('button', { name: 'Continue' }).click()
    await page.getByRole('button', { name: 'Skip this step' }).click()
    await page.getByRole('button', { name: 'What can you do for me?' }).click()
    await expect(page).toHaveURL(/\/chat(\/|$)/, { timeout: 30_000 })
  })

  await stage('first_reply', async () => {
    const reply = page.getByRole('button', { name: 'Copy reply' })
    const failed = page.getByTestId('error')
    await expect(reply.or(failed).first()).toBeVisible({ timeout: 120_000 })
    if (await failed.isVisible()) throw new Error(`chat turn failed: ${await failed.innerText()}`)
  })

  await stage('sample_mission_created', async () => {
    await page.goto('/')
    await expect(page.getByText('Done: Send your first chat')).toBeVisible({ timeout: 30_000 })
    await page.getByRole('button', { name: 'Run a sample mission' }).click()
    await expect(page).toHaveURL(/\/missions\/[0-9a-f-]+$/, { timeout: 30_000 })
    missionId = new URL(page.url()).pathname.split('/').pop() ?? ''
    console.log(`canary-onboarding: sample mission ${missionId}`)
  })

  await stage('sample_mission_terminal', async () => {
    const start = Date.now()
    const limitMs = 8 * 60_000
    for (;;) {
      const m = await missionState(page, missionId)
      const t = Math.round((Date.now() - start) / 1000)
      console.log(`canary-onboarding: mission ${m.phase}/${m.status} (t+${t}s)`)
      if (m.phase === 'done') return
      if (m.phase === 'failed') {
        const events = await lastEvents(page, missionId)
        console.log(events)
        throw new Error('sample mission failed')
      }
      if (m.status === 'paused' || m.status === 'waiting_for_input') {
        console.log(await lastEvents(page, missionId))
        throw new Error(`sample mission parked (${m.status}); the canary must run unattended`)
      }
      if (Date.now() - start > limitMs) throw new Error(`sample mission not terminal after 8 minutes (${m.phase}/${m.status})`)
      await page.waitForTimeout(5_000)
    }
  })

  await stage('checklist_ticks', async () => {
    await page.goto('/')
    for (const title of ['Add a model provider', 'Send your first chat', 'Run your first mission']) {
      await expect(page.getByText(`Done: ${title}`)).toBeVisible({ timeout: 30_000 })
    }
    const summary = await page.getByText(/^\d+ of \d+ done$/).innerText()
    const done = Number(summary.split(' ')[0])
    if (done < 3) throw new Error(`checklist shows ${summary}, want at least 3 done`)
  })
})
