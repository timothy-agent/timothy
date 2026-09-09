import * as React from "react"

import { cn } from "@/lib/utils"

function Input({
  className,
  type,
  size = "default",
  ...props
}: Omit<React.ComponentProps<"input">, "size"> & {
  size?: "default" | "sm"
}) {
  return (
    <input
      type={type}
      data-slot="input"
      data-size={size}
      className={cn(
        "flex w-full min-w-0 rounded-md border border-input bg-card text-sm transition-[border-color,box-shadow,background-color] duration-100 ease-out outline-none file:inline-flex file:h-6 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground placeholder:text-muted-foreground focus-visible:border-brand focus-visible:glow disabled:pointer-events-none disabled:cursor-not-allowed disabled:border-transparent disabled:bg-muted disabled:text-muted-foreground aria-invalid:border-destructive aria-invalid:ring-1 aria-invalid:ring-destructive",
        // Plain utilities, not data-[size] variants: variant utilities
        // sort after plain ones in the generated CSS, so a caller's
        // h-10 or pl-8 would never win against them.
        size === "sm" ? "h-8 px-2.5" : "h-9 px-3.5",
        className
      )}
      {...props}
    />
  )
}

export { Input }
