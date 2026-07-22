import type { AdminProvider } from '../../api/types'

// ProviderPreset is one entry of the declarative provider registry:
// the add dialog, tile grid, and card branding all render from these —
// adding a provider preset needs no form code.
export interface ProviderPreset {
  id: string
  name: string
  driver: 'openaicompat' | 'anthropic' | 'bedrock'
  description: string
  // Sprite symbol in ProviderLogo; custom endpoints render a glyph.
  logo?: string
  brandColor: string
  // Default base_url ('' lets the driver default apply; bedrock keeps
  // the region there instead — see the registry contract).
  baseURL: string
  region?: string
  requiresKey: boolean
  defaultRef?: string
  keyPlaceholder?: string
  keyHint?: string
  // Prefill for the validation model — editable in the dialog, becomes
  // the first declared model and the default on create.
  validateModel: string
}

export const providerPresets: ProviderPreset[] = [
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
    validateModel: 'gpt-4o-mini',
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
    keyHint: 'Create one at console.anthropic.com — keys start with sk-ant-.',
    validateModel: 'claude-haiku-4-5',
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
    requiresKey: false,
    validateModel: 'amazon.nova-lite-v1:0',
  },
  {
    id: 'glm',
    name: 'GLM (Z.ai)',
    driver: 'openaicompat',
    description: "Zhipu's GLM models",
    // No logo: Z.ai/Zhipu's mark isn't in the simple-icons registry —
    // falls back to the neutral glyph rather than guess at an
    // unverified brand mark.
    brandColor: '#4B5563',
    baseURL: 'https://api.z.ai/api/paas/v4',
    requiresKey: true,
    defaultRef: 'ZAI_API_KEY',
    keyPlaceholder: 'paste key',
    keyHint: 'Create one at z.ai.',
    validateModel: 'glm-5.2',
  },
  {
    id: 'grok',
    name: 'Grok (xAI)',
    driver: 'openaicompat',
    description: "xAI's Grok models",
    // No logo: xAI's mark isn't in the simple-icons registry and their
    // site blocks scraping — falls back to the neutral glyph rather
    // than guess at an unverified brand mark.
    brandColor: '#4B5563',
    baseURL: 'https://api.x.ai/v1',
    requiresKey: true,
    defaultRef: 'XAI_API_KEY',
    keyPlaceholder: 'xai-…',
    keyHint: 'Create one at console.x.ai — keys start with xai-.',
    validateModel: 'grok-4',
  },
  {
    id: 'mistral',
    name: 'Mistral',
    driver: 'openaicompat',
    description: 'European frontier models',
    logo: 'mistral',
    brandColor: '#FF7000',
    baseURL: 'https://api.mistral.ai/v1',
    requiresKey: true,
    defaultRef: 'MISTRAL_API_KEY',
    keyHint: 'Create one at console.mistral.ai.',
    validateModel: 'mistral-small-latest',
  },
  {
    id: 'openrouter',
    name: 'OpenRouter',
    driver: 'openaicompat',
    description: 'One key, many providers',
    logo: 'openrouter',
    brandColor: '#6467F2',
    baseURL: 'https://openrouter.ai/api/v1',
    requiresKey: true,
    defaultRef: 'OPENROUTER_API_KEY',
    keyPlaceholder: 'sk-or-…',
    keyHint: 'Create one at openrouter.ai/keys — keys start with sk-or-.',
    validateModel: 'openrouter/auto',
  },
  {
    id: 'ollama',
    name: 'Ollama',
    driver: 'openaicompat',
    description: 'Local models, no key needed',
    logo: 'ollama',
    brandColor: '#4B5563',
    // The gateway runs in Docker; host.docker.internal reaches an
    // Ollama serving on the host machine.
    baseURL: 'http://host.docker.internal:11434/v1',
    requiresKey: false,
    validateModel: 'llama3.2',
  },
  {
    id: 'custom',
    name: 'Custom endpoint',
    driver: 'openaicompat',
    description: 'Any OpenAI-compatible URL',
    brandColor: '#4B5563',
    baseURL: '',
    requiresKey: true,
    keyHint: 'Optional — leave empty for endpoints without auth.',
    validateModel: '',
  },
]

// matchPreset finds the preset a configured provider was (probably)
// created from, for card branding: exact drivers first, then base_url
// host, then the custom fallback.
export function matchPreset(p: AdminProvider): ProviderPreset {
  const custom = providerPresets.find((x) => x.id === 'custom')!
  if (p.driver === 'anthropic' || p.driver === 'bedrock') {
    return providerPresets.find((x) => x.driver === p.driver) ?? custom
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
