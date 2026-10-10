// Writes routes.generated.json from src/routes.ts and the settings
// areas. --check exits non-zero when the committed file is stale.
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { routesExport } from '../src/routesExport.ts'

const target = fileURLToPath(new URL('../routes.generated.json', import.meta.url))
const json = JSON.stringify(routesExport(), null, 2) + '\n'

if (process.argv.includes('--check')) {
  let current = ''
  try {
    current = readFileSync(target, 'utf8')
  } catch {
    // A missing file is stale too.
  }
  if (current !== json) {
    console.error('routes.generated.json is stale: run npm run routes:export and commit it')
    process.exit(1)
  }
  console.log('ok: routes.generated.json is current')
} else {
  writeFileSync(target, json)
  console.log('wrote routes.generated.json')
}
