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
      <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
      <p className="mt-2 text-sm text-muted-foreground">{description}</p>
      {children ?? (
        <div className="mt-8 rounded-xl border border-dashed p-12 text-center text-sm text-muted-foreground">
          Nothing here yet.
        </div>
      )}
    </div>
  )
}
