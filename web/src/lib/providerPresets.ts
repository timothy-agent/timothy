import type { AdminProvider } from '../api/types'

// ProviderPreset is one entry of the declarative provider registry:
// the add dialog, tile grid, and card branding all render from these —
// adding a provider preset needs no form code.
export interface ProviderPreset {
  id: string
  name: string
  driver: 'openaicompat' | 'anthropic' | 'bedrock' | 'claude-cli' | 'cursor-cli' | 'jev'
  description: string
  // Sprite symbol in ProviderLogo; custom endpoints render a glyph.
  logo?: string
  brandColor: string
  // Default base_url ('' lets the driver default apply); unused for
  // bedrock, whose region lives in options.region instead (see `region`
  // below and the registry contract).
  baseURL: string
  // Default AWS region for the bedrock preset's options.region dropdown.
  region?: string
  requiresKey: boolean
  defaultRef?: string
  keyPlaceholder?: string
  keyHint?: string
  // Provider's own key/API-key management page — rendered as a link
  // right where the operator is about to paste one in.
  keyURL?: string
  // Prefill for the validation model — editable in the dialog, becomes
  // the first declared model and the default on create.
  validateModel: string
  // oauth marks a preset whose key Timothy mints through a browser
  // login (D-163): the add form offers Connect instead of a paste box.
  oauth?: 'openrouter'
}

// bedrockRegions lists AWS regions where Bedrock serves models, for the
// provider's region dropdown (options.region). Curated to Bedrock-served
// regions, not every AWS region — extend as AWS does.
export const bedrockRegions: { value: string; label: string }[] = [
  { value: 'us-east-1', label: 'us-east-1 (N. Virginia)' },
  { value: 'us-east-2', label: 'us-east-2 (Ohio)' },
  { value: 'us-west-2', label: 'us-west-2 (Oregon)' },
  { value: 'ca-central-1', label: 'ca-central-1 (Canada)' },
  { value: 'sa-east-1', label: 'sa-east-1 (São Paulo)' },
  { value: 'eu-central-1', label: 'eu-central-1 (Frankfurt)' },
  { value: 'eu-west-1', label: 'eu-west-1 (Ireland)' },
  { value: 'eu-west-2', label: 'eu-west-2 (London)' },
  { value: 'eu-west-3', label: 'eu-west-3 (Paris)' },
  { value: 'eu-north-1', label: 'eu-north-1 (Stockholm)' },
  { value: 'ap-northeast-1', label: 'ap-northeast-1 (Tokyo)' },
  { value: 'ap-northeast-2', label: 'ap-northeast-2 (Seoul)' },
  { value: 'ap-south-1', label: 'ap-south-1 (Mumbai)' },
  { value: 'ap-southeast-1', label: 'ap-southeast-1 (Singapore)' },
  { value: 'ap-southeast-2', label: 'ap-southeast-2 (Sydney)' },
]

// awsRegions lists AWS regions for the ASM backend's region dropdown.
// ASM isn't limited to Bedrock's serving footprint, so this is the
// fuller commercial-partition region list, not bedrockRegions.
export const awsRegions: { value: string; label: string }[] = [
  { value: 'us-east-1', label: 'us-east-1 (N. Virginia)' },
  { value: 'us-east-2', label: 'us-east-2 (Ohio)' },
  { value: 'us-west-1', label: 'us-west-1 (N. California)' },
  { value: 'us-west-2', label: 'us-west-2 (Oregon)' },
  { value: 'ca-central-1', label: 'ca-central-1 (Canada)' },
  { value: 'sa-east-1', label: 'sa-east-1 (São Paulo)' },
  { value: 'eu-central-1', label: 'eu-central-1 (Frankfurt)' },
  { value: 'eu-central-2', label: 'eu-central-2 (Zurich)' },
  { value: 'eu-west-1', label: 'eu-west-1 (Ireland)' },
  { value: 'eu-west-2', label: 'eu-west-2 (London)' },
  { value: 'eu-west-3', label: 'eu-west-3 (Paris)' },
  { value: 'eu-north-1', label: 'eu-north-1 (Stockholm)' },
  { value: 'eu-south-1', label: 'eu-south-1 (Milan)' },
  { value: 'eu-south-2', label: 'eu-south-2 (Spain)' },
  { value: 'me-central-1', label: 'me-central-1 (UAE)' },
  { value: 'me-south-1', label: 'me-south-1 (Bahrain)' },
  { value: 'af-south-1', label: 'af-south-1 (Cape Town)' },
  { value: 'ap-northeast-1', label: 'ap-northeast-1 (Tokyo)' },
  { value: 'ap-northeast-2', label: 'ap-northeast-2 (Seoul)' },
  { value: 'ap-northeast-3', label: 'ap-northeast-3 (Osaka)' },
  { value: 'ap-south-1', label: 'ap-south-1 (Mumbai)' },
  { value: 'ap-south-2', label: 'ap-south-2 (Hyderabad)' },
  { value: 'ap-southeast-1', label: 'ap-southeast-1 (Singapore)' },
  { value: 'ap-southeast-2', label: 'ap-southeast-2 (Sydney)' },
  { value: 'ap-southeast-3', label: 'ap-southeast-3 (Jakarta)' },
  { value: 'ap-southeast-4', label: 'ap-southeast-4 (Melbourne)' },
  { value: 'ap-east-1', label: 'ap-east-1 (Hong Kong)' },
]

export const providerPresets: ProviderPreset[] = [
  {
    id: 'openrouter',
    name: 'OpenRouter',
    driver: 'openaicompat',
    description: 'Free models to start, one login',
    logo: 'openrouter',
    brandColor: '#7624F4',
    baseURL: 'https://openrouter.ai/api/v1',
    requiresKey: true,
    defaultRef: 'OPENROUTER_API_KEY',
    keyPlaceholder: 'sk-or-v1-…',
    keyHint: 'Create one at openrouter.ai/keys, keys start with sk-or-.',
    keyURL: 'https://openrouter.ai/keys',
    validateModel: 'nvidia/nemotron-3-super-120b-a12b:free',
    oauth: 'openrouter',
  },
  {
    id: 'openai',
    name: 'OpenAI',
    driver: 'openaicompat',
    description: 'GPT and o-series models',
    logo: 'openai',
    brandColor: '#10A37F',
    baseURL: 'https://api.openai.com/v1',
    requiresKey: true,
    defaultRef: 'OPENAI_API_KEY',
    keyPlaceholder: 'sk-…',
    keyHint: 'Create one at platform.openai.com/api-keys.',
    keyURL: 'https://platform.openai.com/api-keys',
    validateModel: 'gpt-4o-mini',
  },
  {
    id: 'chatgpt',
    name: 'OpenAI Subscription',
    driver: 'openaicompat',
    description: 'ChatGPT Plus or Pro, via local proxy',
    logo: 'openai',
    brandColor: '#10A37F',
    baseURL: 'http://cliproxy:8317/v1',
    requiresKey: false,
    keyHint: 'Zero per-token cost. Managed via CLIProxyAPI.',
    validateModel: 'gpt-4o',
  },
  {
    id: 'anthropic',
    name: 'Anthropic',
    driver: 'anthropic',
    description: 'Claude models, direct API',
    logo: 'anthropic',
    brandColor: '#D97757',
    baseURL: '',
    requiresKey: true,
    defaultRef: 'ANTHROPIC_API_KEY',
    keyPlaceholder: 'sk-ant-…',
    keyHint: 'Create one at console.anthropic.com, keys start with sk-ant-.',
    keyURL: 'https://console.anthropic.com/settings/keys',
    validateModel: 'claude-haiku-4-5',
  },
  {
    id: 'claude',
    name: 'Claude Subscription',
    driver: 'openaicompat',
    description: 'Claude Pro/Team via local proxy',
    logo: 'anthropic',
    brandColor: '#D97757',
    baseURL: 'http://cliproxy:8317/v1',
    requiresKey: false,
    keyHint: 'Zero per-token cost. Managed via CLIProxyAPI.',
    validateModel: 'claude-3-7-sonnet-20250219',
  },
  {
    id: 'cursor',
    name: 'Cursor',
    driver: 'cursor-cli',
    description: 'Cursor CLI for coding missions',
    brandColor: '#000000',
    baseURL: '',
    requiresKey: true,
    defaultRef: 'CURSOR_API_KEY',
    keyHint: 'Create an API key in Cursor settings.',
    keyURL: 'https://cursor.com/settings',
    validateModel: 'composer-2.5',
  },
  {
    id: 'bedrock',
    name: 'AWS Bedrock',
    driver: 'bedrock',
    description: 'Amazon Nova models via AWS',
    logo: 'bedrock',
    brandColor: '#FF9900',
    baseURL: '',
    region: 'us-east-1',
    requiresKey: true,
    keyHint: 'Create an IAM user with Bedrock access and generate an access key pair.',
    keyURL: 'https://console.aws.amazon.com/iam/home#/users',
    validateModel: 'amazon.nova-lite-v1:0',
  },
  {
    id: 'glm',
    name: 'GLM (Z.ai)',
    driver: 'openaicompat',
    description: "Zhipu's GLM models",
    logo: 'zai',
    brandColor: '#000000',
    baseURL: 'https://api.z.ai/api/paas/v4',
    requiresKey: true,
    defaultRef: 'ZAI_API_KEY',
    keyPlaceholder: 'paste key',
    keyHint: 'Create one at z.ai.',
    keyURL: 'https://z.ai/manage-apikey/apikey-list',
    validateModel: 'glm-4.7-flash',
  },
  {
    id: 'grok',
    name: 'Grok (xAI)',
    driver: 'openaicompat',
    description: "xAI's Grok models",
    logo: 'grok',
    brandColor: '#000000',
    baseURL: 'https://api.x.ai/v1',
    requiresKey: true,
    defaultRef: 'XAI_API_KEY',
    keyPlaceholder: 'xai-…',
    keyHint: 'Create one at console.x.ai, keys start with xai-.',
    keyURL: 'https://console.x.ai',
    validateModel: 'grok-4',
  },
  {
    id: 'deepseek',
    name: 'DeepSeek',
    driver: 'openaicompat',
    description: "DeepSeek's own API",
    logo: 'deepseek',
    brandColor: '#4D6BFE',
    baseURL: 'https://api.deepseek.com',
    requiresKey: true,
    defaultRef: 'DEEPSEEK_API_KEY',
    keyPlaceholder: 'sk-…',
    keyHint: 'Create one at platform.deepseek.com, keys start with sk-.',
    keyURL: 'https://platform.deepseek.com/api_keys',
    validateModel: 'deepseek-flash',
  },
  {
    id: 'nebius',
    name: 'Nebius',
    driver: 'openaicompat',
    description: 'Open models on Nebius Token Factory',
    logo: 'nebius',
    brandColor: '#052B42',
    baseURL: 'https://api.tokenfactory.nebius.com/v1',
    requiresKey: true,
    defaultRef: 'NEBIUS_API_KEY',
    keyPlaceholder: 'paste key',
    keyHint: 'Create one at tokenfactory.nebius.com.',
    keyURL: 'https://tokenfactory.nebius.com',
    validateModel: 'deepseek-ai/DeepSeek-V4-Flash',
  },
  {
    id: 'ollama',
    name: 'Ollama',
    driver: 'openaicompat',
    description: 'Local models, no key needed',
    logo: 'ollama',
    brandColor: '#4B5563',
    // Ollama runs natively on the host for GPU access (the compose
    // service was removed); the gateway reaches it via Docker's host
    // alias.
    baseURL: 'http://host.docker.internal:11434/v1',
    requiresKey: false,
    validateModel: 'qwen3:8b',
  },
  {
    id: 'jev',
    name: 'TypeSafe Jev',
    driver: 'jev',
    description: 'Hosted decision model for fast routing',
    brandColor: '#2563EB',
    baseURL: 'https://api.typesafe.ai',
    requiresKey: true,
    defaultRef: 'JEV_API_KEY',
    keyPlaceholder: 'jv_live_…',
    keyHint: 'Create an API key at typesafe.ai.',
    keyURL: 'https://typesafe.ai',
    validateModel: 'jev-latest',
  },
  {
    id: 'custom',
    name: 'Custom endpoint',
    driver: 'openaicompat',
    description: 'Any OpenAI-compatible URL',
    brandColor: '#4B5563',
    baseURL: '',
    requiresKey: true,
    keyHint: 'Optional, leave empty for endpoints without auth.',
    validateModel: '',
  },
]

// MatchPresetSource is the subset of a provider row matchPreset reads -
// widened so it also accepts an ExecutionPlanEntry, which carries the
// same driver/kind/base_url fields trimmed from the gateway resolve
// response.
export type MatchPresetSource = Pick<AdminProvider, 'kind' | 'driver' | 'base_url'> &
  Partial<Pick<AdminProvider, 'name' | 'default_model'>>

// matchPreset finds the preset a configured provider was (probably)
// created from, for card branding: exact drivers first, then base_url
// host, then the custom fallback.
export function matchPreset(p: MatchPresetSource): ProviderPreset {
  const custom = providerPresets.find((x) => x.id === 'custom')!
  // kind='cli' rows (D-051) are subscription auth, not a separate
  // preset per driver; branding folds into the matching CLI card
  // (claude-cli -> anthropic, cursor-cli -> cursor).
  if (p.kind === 'cli') {
    if (p.driver === 'cursor-cli') return providerPresets.find((x) => x.id === 'cursor') ?? custom
    return providerPresets.find((x) => x.id === 'anthropic') ?? custom
  }
  if (p.driver === 'anthropic' || p.driver === 'bedrock' || p.driver === 'jev') {
    return providerPresets.find((x) => x.driver === p.driver) ?? custom
  }
  // Ollama's fixed default port (11434) is a stronger signal than
  // hostname, which varies across deployments (ollama, localhost,
  // host.docker.internal).
  try {
    const u = new URL(p.base_url)
    if (u.port === '11434') {
      return providerPresets.find((x) => x.id === 'ollama') ?? custom
    }
    if (u.port === '8317' || u.hostname === 'cliproxy') {
      const id = cliproxyPresetId(p.name ?? '', p.default_model ?? '')
      return providerPresets.find((x) => x.id === id) ?? custom
    }
  } catch {
    // fall through to host matching below
  }
  const byURL = providerPresets.find((x) => {
    if (!x.baseURL || x.id === 'custom') return false
    try {
      return new URL(x.baseURL).host === new URL(p.base_url).host
    } catch {
      return false
    }
  })
  return byURL ?? custom
}

// cliproxyPresetId brands a cliproxy row as Claude or ChatGPT from the
// same token signals the gateway uses to pick a catalog section. An
// ambiguous row stays ChatGPT, which is the historical default.
function cliproxyPresetId(name: string, model: string): 'claude' | 'chatgpt' {
  const tokens = `${name} ${model}`.toLowerCase().split(/[^a-z0-9]+/).filter(Boolean)
  const anthropic = tokens.some(
    (t) => t === 'anthropic' || t === 'sonnet' || t === 'opus' || t === 'haiku' || t.startsWith('claude'),
  )
  const openai = tokens.some(
    (t) => t === 'openai' || t === 'chatgpt' || t === 'codex' || t.startsWith('gpt') || t === 'o1' || t === 'o3' || t === 'o4',
  )
  if (anthropic && !openai) return 'claude'
  return 'chatgpt'
}
