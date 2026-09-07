import * as React from "react"
import { ToggleGroup as ToggleGroupPrimitive } from "radix-ui"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

const toggleGroupVariants = cva(
  "inline-flex items-center rounded-md border border-input bg-card p-0.5",
  {
    variants: {
      size: {
        default: "h-9",
        sm: "h-8",
      },
    },
    defaultVariants: {
      size: "default",
    },
  }
)

const ToggleGroupContext = React.createContext<{ size: "default" | "sm" }>({
  size: "default",
})

function ToggleGroup({
  className,
  size = "default",
  children,
  ...props
}: React.ComponentProps<typeof ToggleGroupPrimitive.Root> &
  VariantProps<typeof toggleGroupVariants>) {
  return (
    <ToggleGroupPrimitive.Root
      data-slot="toggle-group"
      className={cn(toggleGroupVariants({ size }), className)}
      {...props}
    >
      <ToggleGroupContext.Provider value={{ size: size ?? "default" }}>
        {children}
      </ToggleGroupContext.Provider>
    </ToggleGroupPrimitive.Root>
  )
}

function ToggleGroupItem({
  className,
  children,
  ...props
}: React.ComponentProps<typeof ToggleGroupPrimitive.Item>) {
  const { size } = React.useContext(ToggleGroupContext)

  return (
    <ToggleGroupPrimitive.Item
      data-slot="toggle-group-item"
      className={cn(
        "inline-flex items-center justify-center rounded-md border-l border-l-transparent text-sm text-muted-foreground outline-none transition-[color,background-color] duration-100 ease-out first:border-l-0 not-first:border-l-input hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50 data-[state=on]:bg-foreground data-[state=on]:font-medium data-[state=on]:text-background",
        size === "sm" ? "h-7 px-2.5" : "h-8 px-3",
        className
      )}
      {...props}
    >
      {children}
    </ToggleGroupPrimitive.Item>
  )
}

export { ToggleGroup, ToggleGroupItem }
