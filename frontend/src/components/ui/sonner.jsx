import * as React from "react"
import { Toaster as Sonner } from "sonner"
import { cn } from "../../lib/utils"

// Sonner's CSS variables point at our tokens. The tokens flip with `.dark` on <html>, so toasts
// follow the theme with no `theme` prop. Callers use `import { toast } from "sonner"`.
const Toaster = React.forwardRef(({ className, style, ...props }, ref) => (
  <Sonner
    ref={ref}
    className={cn("toaster", className)}
    position="bottom-right"
    style={{
      "--normal-bg": "hsl(var(--popover))",
      "--normal-text": "hsl(var(--popover-foreground))",
      "--normal-border": "hsl(var(--border))",
      "--border-radius": "var(--radius)",
      ...style,
    }}
    {...props}
  />
))
Toaster.displayName = "Toaster"

export { Toaster }
