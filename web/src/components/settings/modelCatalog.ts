// One catalog entry: the id actually submitted to the API, and a
// friendly name shown in the UI (id stays visible too, just
// secondary — some ids are opaque, like Bedrock's, so the exact
// string being picked should never be hidden entirely).
//
// prices: USD per million tokens, cross-checked against OpenRouter's
// public /api/v1/models pricing feed (data source of record — the
// providers' own marketing pages are JS-rendered and have repeatedly
// returned fabricated figures under WebFetch/WebSearch). Omitted where
// no confident match exists — never guessed.
export interface CatalogModel {
  id: string
  name: string
  prices?: { input_per_mtok: number; output_per_mtok: number }
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
    { id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol', prices: { input_per_mtok: 5.0, output_per_mtok: 30.0 } },
    { id: 'gpt-5.6-sol-pro', name: 'GPT-5.6 Sol Pro', prices: { input_per_mtok: 5.0, output_per_mtok: 30.0 } },
    { id: 'gpt-5.6-terra', name: 'GPT-5.6 Terra', prices: { input_per_mtok: 2.5, output_per_mtok: 15.0 } },
    { id: 'gpt-5.6-terra-pro', name: 'GPT-5.6 Terra Pro', prices: { input_per_mtok: 2.5, output_per_mtok: 15.0 } },
    { id: 'gpt-5.6-luna', name: 'GPT-5.6 Luna', prices: { input_per_mtok: 1.0, output_per_mtok: 6.0 } },
    { id: 'gpt-5.3-codex', name: 'GPT-5.3 Codex', prices: { input_per_mtok: 1.75, output_per_mtok: 14.0 } },
    { id: 'o3', name: 'o3', prices: { input_per_mtok: 2.0, output_per_mtok: 8.0 } },
    { id: 'o3-pro', name: 'o3 Pro', prices: { input_per_mtok: 20.0, output_per_mtok: 80.0 } },
  ],
  anthropic: [
    { id: 'claude-opus-4-8', name: 'Claude Opus 4.8', prices: { input_per_mtok: 5.0, output_per_mtok: 25.0 } },
    { id: 'claude-opus-4-7', name: 'Claude Opus 4.7', prices: { input_per_mtok: 5.0, output_per_mtok: 25.0 } },
    { id: 'claude-sonnet-5', name: 'Claude Sonnet 5', prices: { input_per_mtok: 2.0, output_per_mtok: 10.0 } },
    { id: 'claude-sonnet-4-6', name: 'Claude Sonnet 4.6', prices: { input_per_mtok: 3.0, output_per_mtok: 15.0 } },
    { id: 'claude-haiku-4-5', name: 'Claude Haiku 4.5', prices: { input_per_mtok: 1.0, output_per_mtok: 5.0 } },
    { id: 'claude-fable-5', name: 'Claude Fable 5', prices: { input_per_mtok: 10.0, output_per_mtok: 50.0 } },
  ],
  // 1P (Amazon) only — Nova and Titan, ACTIVE lifecycle, ON_DEMAND or
  // INFERENCE_PROFILE inference type only (skips PROVISIONED-only
  // capacity variants like nova-pro-v1:0:300k). Pulled from
  // `aws bedrock list-foundation-models --by-provider amazon`.
  bedrock: [
    { id: 'amazon.nova-micro-v1:0', name: 'Nova Micro', prices: { input_per_mtok: 0.035, output_per_mtok: 0.14 } },
    { id: 'amazon.nova-lite-v1:0', name: 'Nova Lite', prices: { input_per_mtok: 0.06, output_per_mtok: 0.24 } },
    { id: 'amazon.nova-pro-v1:0', name: 'Nova Pro', prices: { input_per_mtok: 0.8, output_per_mtok: 3.2 } },
    { id: 'amazon.nova-2-lite-v1:0', name: 'Nova 2 Lite', prices: { input_per_mtok: 0.3, output_per_mtok: 2.5 } },
    // Embedding models: single per-input-token rate, no output price.
    // v1 has no published price found — left unpriced rather than guessed.
    { id: 'amazon.titan-embed-text-v1', name: 'Titan Embeddings G1 - Text' },
    // AWS's own docs, quoted directly via aws.amazon.com/blogs ML post:
    // "$0.00002 per 1,000 input tokens" = $0.02 per 1M.
    {
      id: 'amazon.titan-embed-text-v2:0',
      name: 'Titan Text Embeddings V2',
      prices: { input_per_mtok: 0.02, output_per_mtok: 0 },
    },
  ],
  glm: [
    // docs.z.ai direct pricing (verified earlier, cross-checked
    // against 2 sources) — OpenRouter re-sells this at a different
    // rate, not the number that matters for a direct GLM provider.
    // glm-4.7-flash is Z.ai's free tier — distinct from the paid
    // glm-4.7-flashx SKU, do not conflate the two.
    { id: 'glm-4.7-flash', name: 'GLM-4.7 Flash', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'glm-5.2', name: 'GLM-5.2', prices: { input_per_mtok: 1.4, output_per_mtok: 4.4 } },
    { id: 'glm-4.6', name: 'GLM-4.6', prices: { input_per_mtok: 0.6, output_per_mtok: 2.2 } },
    { id: 'glm-4.5', name: 'GLM-4.5', prices: { input_per_mtok: 0.6, output_per_mtok: 2.2 } },
    { id: 'glm-4.5-air', name: 'GLM-4.5 Air', prices: { input_per_mtok: 0.2, output_per_mtok: 1.1 } },
  ],
  grok: [
    { id: 'grok-4.5', name: 'Grok 4.5', prices: { input_per_mtok: 2.0, output_per_mtok: 6.0 } },
    { id: 'grok-4.3', name: 'Grok 4.3', prices: { input_per_mtok: 1.25, output_per_mtok: 2.5 } },
    // Model exists (confirmed on pricepertoken.com's xAI provider
    // page) but its price cell there is an unpopulated placeholder
    // ($0.000 in grey, not the real orange/green price styling used
    // elsewhere on the same page) — left unpriced rather than
    // reporting a fabricated free tier.
    { id: 'grok-4.1-fast', name: 'Grok 4.1 Fast' },
    // pricepertoken.com, xAI's own listed rate.
    { id: 'grok-code-fast-1', name: 'Grok Code Fast 1', prices: { input_per_mtok: 0.2, output_per_mtok: 1.5 } },
  ],
  ollama: [
    { id: 'llama3.2', name: 'Llama 3.2', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'llama3.1', name: 'Llama 3.1', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'qwen2.5', name: 'Qwen 2.5', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'qwen2.5:7b', name: 'Qwen 2.5 7B', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'mistral', name: 'Mistral', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'deepseek-r1', name: 'DeepSeek R1', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'gemma2', name: 'Gemma 2', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'phi4', name: 'Phi-4', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
    { id: 'codellama', name: 'Code Llama', prices: { input_per_mtok: 0, output_per_mtok: 0 } },
  ],
  custom: [],
}
