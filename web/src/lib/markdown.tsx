import { ExternalLink } from 'lucide-react'
import type { ComponentPropsWithoutRef } from 'react'
import rehypeRaw from 'rehype-raw'
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize'
import type { Options as SanitizeSchema } from 'rehype-sanitize'
import remarkGfm from 'remark-gfm'
import { Link } from 'react-router'
import type { Components } from 'react-markdown'
import type { PluggableList } from 'unified'
import { CodeBlock } from '../components/CodeBlock'
import { isAppPath } from './appPath'

// Shared ReactMarkdown plugin config, used by every markdown call site.
// rehypeRaw parses raw HTML nodes into the tree (react-markdown skips
// them by default); rehypeSanitize then strips anything dangerous
// (script tags, event handler attributes, javascript: URLs) before render.
//
// defaultSchema already covers what this app's markdown needs: `align`
// and `width`/`height` are allowed on all elements (including p/img/td/th),
// `code` keeps its `language-*` className (CodeBlock/mermaid detection
// depends on this), and img `src` is restricted to http/https. Used as-is,
// no extension needed. The default href protocols (http, https, mailto,
// tel, ...) drop javascript: and data: URLs but keep relative hrefs;
// those are gated by isAppPath in the `a` component below.
const schema: SanitizeSchema = defaultSchema

export const remarkPlugins: PluggableList = [remarkGfm]
export const rehypePlugins: PluggableList = [rehypeRaw, [rehypeSanitize, schema]]

// Shared ReactMarkdown component overrides, used by every Chat markdown
// call site: `pre` renders fenced code through CodeBlock (shiki
// highlighting, mermaid detection); `table` wraps GFM tables in a
// horizontally scrolling div so a wide table scrolls in place instead
// of pushing the column wider.
export const markdownComponents: Components = {
  a: ({ href, children, node: _node, ...rest }: ComponentPropsWithoutRef<'a'> & { node?: unknown }) => {
    if (href && /^https?:\/\//i.test(href))
      return (
        <a {...rest} href={href} target="_blank" rel="noopener noreferrer">
          {children}
        </a>
      )
    // Relative href (no scheme): navigate in-app when it names a known
    // route, otherwise show the text and the raw href with no anchor.
    if (href && !/^[a-z][a-z0-9+.-]*:/i.test(href)) {
      if (isAppPath(href))
        return (
          <Link to={href} title="Open in Timothy" className={rest.className}>
            {children}
            <ExternalLink className="ml-0.5 inline size-3 align-baseline" aria-hidden />
          </Link>
        )
      return (
        <span>
          {children} ({href})
        </span>
      )
    }
    return (
      <a {...rest} href={href}>
        {children}
      </a>
    )
  },
  pre: CodeBlock,
  table: (props: ComponentPropsWithoutRef<'table'>) => (
    <div className="overflow-x-auto">
      <table {...props} />
    </div>
  ),
}
