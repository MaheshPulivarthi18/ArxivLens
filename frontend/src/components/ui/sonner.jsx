import * as React from "react"
import { Toaster as Sonner } from "sonner"
import { cn } from "../../lib/utils"

// Sonner's CSS variables point at our tokens. The tokens flip with `.dark` on <html>, so toasts
// follow the theme with no `theme` prop. Callers use `import { toast } from "sonner"`.
// Sonner hard-codes the description colour (#3f3f3f) unless its own theme is dark, so the
// description gets a token class too. The `!` beats sonner's (0,3,0) selector.
const Toaster = React.forwardRef(({ className, style, toastOptions, ...props }, ref) => (
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
    toastOptions={{
      ...toastOptions,
      classNames: {
        ...toastOptions?.classNames,
        description: cn("!text-muted-foreground", toastOptions?.classNames?.description),
      },
    }}
  />
))
Toaster.displayName = "Toaster"

export { Toaster }
