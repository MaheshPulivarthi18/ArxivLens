import * as React from "react"
import * as TooltipPrimitive from "@radix-ui/react-tooltip"
import { cn } from "../../lib/utils"

// Tooltips are supplementary only: never the sole carrier of a status or a link.
const TooltipProvider = ({ delayDuration = 300, ...props }) => (
  <TooltipPrimitive.Provider delayDuration={delayDuration} {...props} />
)
TooltipProvider.displayName = "TooltipProvider"

// Wraps Root in its own Provider so callers don't need a top-level provider.
const Tooltip = ({ delayDuration = 300, ...props }) => (
  <TooltipPrimitive.Provider delayDuration={delayDuration}>
    <TooltipPrimitive.Root {...props} />
  </TooltipPrimitive.Provider>
)
Tooltip.displayName = TooltipPrimitive.Root.displayName

// Unstyled: callers style the trigger (usually a Button via asChild, which carries the focus ring).
const TooltipTrigger = TooltipPrimitive.Trigger

const TooltipContent = React.forwardRef(({ className, sideOffset = 4, ...props }, ref) => (
  <TooltipPrimitive.Portal>
    <TooltipPrimitive.Content
      ref={ref}
      sideOffset={sideOffset}
      className={cn(
        "z-50 max-w-xs rounded-md border border-border bg-popover px-2.5 py-1.5 font-sans text-xs text-popover-foreground shadow-sm",
        className
      )}
      {...props}
    />
  </TooltipPrimitive.Portal>
))
TooltipContent.displayName = TooltipPrimitive.Content.displayName

export { TooltipProvider, Tooltip, TooltipTrigger, TooltipContent }
