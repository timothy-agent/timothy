import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"
import { CircleCheck, TriangleAlert, Info, CircleX, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

const alertVariants = cva(
  "relative grid grid-cols-[16px_1fr] gap-x-3 rounded-md border border-border border-l-[3px] bg-card p-4 text-sm",
  {
    variants: {
      tone: {
        neutral: "border-l-border",
        good: "border-l-good",
        warning: "border-l-warning",
        info: "border-l-info",
        destructive: "border-l-destructive",
      },
    },
    defaultVariants: {
      tone: "neutral",
    },
  }
)

const toneIconClassName: Record<string, string> = {
  neutral: "text-muted-foreground",
  good: "text-good",
  warning: "text-warning",
  info: "text-info",
  destructive: "text-destructive",
}

const toneIcon: Record<string, LucideIcon | null> = {
  neutral: null,
  good: CircleCheck,
  warning: TriangleAlert,
  info: Info,
  destructive: CircleX,
}

function Alert({
  className,
  tone = "neutral",
  icon,
  children,
  ...props
}: React.ComponentProps<"div"> &
  VariantProps<typeof alertVariants> & {
    icon?: React.ReactNode | null
  }) {
  const resolvedTone = tone ?? "neutral"
  const Icon = toneIcon[resolvedTone]
  const renderedIcon =
    icon === null ? null : icon !== undefined ? icon : Icon ? <Icon className={cn("size-4", toneIconClassName[resolvedTone])} /> : null

  return (
    <div
      data-slot="alert"
      data-tone={resolvedTone}
      role={resolvedTone === "destructive" ? "alert" : "status"}
      className={cn(alertVariants({ tone }), className)}
      {...props}
    >
      <div className="row-span-2 flex items-start pt-0.5">{renderedIcon}</div>
      {children}
    </div>
  )
}

function AlertTitle({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-title"
      className={cn("text-sm leading-5 font-semibold", className)}
      {...props}
    />
  )
}

function AlertDescription({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      data-slot="alert-description"
      className={cn("text-sm text-muted-foreground", className)}
      {...props}
    />
  )
}

export { Alert, AlertTitle, AlertDescription }
