// One catalog entry: the id actually submitted to the API, and a
// friendly name shown in the UI (id stays visible too, just
// secondary — some ids are opaque, like Bedrock's, so the exact
// string being picked should never be hidden entirely).
export interface CatalogModel {
  id: string
  name: string
}

// Static, hand-maintained model suggestions per provider preset.
// Providers don't expose a uniform "list your models" API (Bedrock has
// none at all; OpenAI/Anthropic's listings carry no capability or
// price data), so this is advisory only — same contract as ModelInput
// suggestions elsewhere: never blocks a free-typed id, just surfaces
// ones likely to exist. Refresh by hand when a provider ships a new
// model family; there's no runtime job keeping this in sync.
export const modelCatalog: Record<string, CatalogModel[]> = {
  openai: [
    { id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol' },
    { id: 'gpt-5.6-sol-pro', name: 'GPT-5.6 Sol Pro' },
    { id: 'gpt-5.6-terra', name: 'GPT-5.6 Terra' },
    { id: 'gpt-5.6-terra-pro', name: 'GPT-5.6 Terra Pro' },
    { id: 'gpt-5.6-luna', name: 'GPT-5.6 Luna' },
    { id: 'gpt-5.3-codex', name: 'GPT-5.3 Codex' },
    { id: 'o3', name: 'o3' },
    { id: 'o3-pro', name: 'o3 Pro' },
  ],
  anthropic: [
    { id: 'claude-opus-4-8', name: 'Claude Opus 4.8' },
    { id: 'claude-opus-4-7', name: 'Claude Opus 4.7' },
    { id: 'claude-sonnet-5', name: 'Claude Sonnet 5' },
    { id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6' },
    { id: 'claude-haiku-4-5', name: 'Claude Haiku 4.5' },
    { id: 'claude-fable-5', name: 'Claude Fable 5' },
  ],
  // 1P (Amazon) only — Nova and Titan, ACTIVE lifecycle, ON_DEMAND or
  // INFERENCE_PROFILE inference type only (skips PROVISIONED-only
  // capacity variants like nova-pro-v1:0:300k). Pulled from
  // `aws bedrock list-foundation-models --by-provider amazon`.
  bedrock: [
    { id: 'amazon.nova-micro-v1:0', name: 'Nova Micro' },
    { id: 'amazon.nova-lite-v1:0', name: 'Nova Lite' },
    { id: 'amazon.nova-pro-v1:0', name: 'Nova Pro' },
    { id: 'amazon.nova-2-lite-v1:0', name: 'Nova 2 Lite' },
    { id: 'amazon.titan-embed-text-v1', name: 'Titan Embeddings G1 - Text' },
    { id: 'amazon.titan-embed-text-v2:0', name: 'Titan Text Embeddings V2' },
  ],
  glm: [
    { id: 'glm-5.2', name: 'GLM-5.2' },
    { id: 'glm-4.6', name: 'GLM-4.6' },
    { id: 'glm-4.5', name: 'GLM-4.5' },
    { id: 'glm-4.5-air', name: 'GLM-4.5 Air' },
  ],
  grok: [
    { id: 'grok-4.5', name: 'Grok 4.5' },
    { id: 'grok-4.3', name: 'Grok 4.3' },
    { id: 'grok-4.1-fast', name: 'Grok 4.1 Fast' },
    { id: 'grok-code-fast-1', name: 'Grok Code Fast 1' },
  ],
  mistral: [
    { id: 'mistral-large-latest', name: 'Mistral Large' },
    { id: 'mistral-medium-latest', name: 'Mistral Medium' },
    { id: 'mistral-small-latest', name: 'Mistral Small' },
    { id: 'codestral-latest', name: 'Codestral' },
    { id: 'devstral-2-latest', name: 'Devstral 2' },
    { id: 'ministral-3-8b-latest', name: 'Ministral 3 8B' },
  ],
  openrouter: [
    { id: 'openrouter/auto', name: 'Auto (best available)' },
    { id: 'anthropic/claude-sonnet-5', name: 'Claude Sonnet 5' },
    { id: 'openai/gpt-5.6-sol', name: 'GPT-5.6 Sol' },
    { id: 'x-ai/grok-4.5', name: 'Grok 4.5' },
    { id: 'meta-llama/llama-3.3-70b-instruct', name: 'Llama 3.3 70B Instruct' },
    { id: 'mistralai/mistral-medium-3.5', name: 'Mistral Medium 3.5' },
    { id: 'z-ai/glm-5.2', name: 'GLM-5.2' },
  ],
  ollama: [
    { id: 'llama3.2', name: 'Llama 3.2' },
    { id: 'llama3.1', name: 'Llama 3.1' },
    { id: 'qwen2.5', name: 'Qwen 2.5' },
    { id: 'mistral', name: 'Mistral' },
    { id: 'deepseek-r1', name: 'DeepSeek R1' },
    { id: 'gemma2', name: 'Gemma 2' },
    { id: 'phi4', name: 'Phi-4' },
    { id: 'codellama', name: 'Code Llama' },
  ],
  custom: [],
}
