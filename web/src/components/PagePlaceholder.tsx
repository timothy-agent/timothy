import { Heading } from './catalyst/heading'
import { Text } from './catalyst/text'

// PagePlaceholder reserves a surface a later phase fills in. It keeps
// navigation honest today without pretending features exist.
export function PagePlaceholder({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children?: React.ReactNode
}) {
  return (
    <div className="mx-auto max-w-5xl py-8">
      <Heading>{title}</Heading>
      <Text className="mt-2">{description}</Text>
      {children ?? (
        <div className="mt-8 rounded-xl border border-dashed border-zinc-300 p-12 text-center text-sm text-zinc-400 dark:border-zinc-700">
          Nothing here yet.
        </div>
      )}
    </div>
  )
}
