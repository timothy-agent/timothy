import rehypeRaw from 'rehype-raw'
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize'
import type { Options as SanitizeSchema } from 'rehype-sanitize'
import remarkGfm from 'remark-gfm'
import type { PluggableList } from 'unified'

// Shared ReactMarkdown plugin config, used by every markdown call site.
// rehypeRaw parses raw HTML nodes into the tree (react-markdown skips
// them by default); rehypeSanitize then strips anything dangerous
// (script tags, event handler attributes, javascript: URLs) before render.
//
// defaultSchema already covers what this app's markdown needs: `align`
// and `width`/`height` are allowed on all elements (including p/img/td/th),
// `code` keeps its `language-*` className (CodeBlock/mermaid detection
// depends on this), and img `src` is restricted to http/https. Used as-is,
// no extension needed.
const schema: SanitizeSchema = defaultSchema

export const remarkPlugins: PluggableList = [remarkGfm]
export const rehypePlugins: PluggableList = [rehypeRaw, [rehypeSanitize, schema]]
