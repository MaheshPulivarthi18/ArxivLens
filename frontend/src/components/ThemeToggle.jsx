import { useEffect, useState } from "react"
import { Monitor, Moon, Sun } from "lucide-react"
import { Button } from "./ui/button"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuLabel, DropdownMenuRadioGroup, DropdownMenuRadioItem, DropdownMenuTrigger,
} from "./ui/dropdown-menu"
import {
  THEME_STORAGE_KEY, THEMES, applyTheme, getStoredTheme, resolveTheme, setStoredTheme, systemPrefersDark, watchSystemTheme,
} from "../lib/theme"

const OPTIONS = {
  light: { label: "Light", Icon: Sun },
  dark: { label: "Dark", Icon: Moon },
  system: { label: "System", Icon: Monitor },
}

export default function ThemeToggle() {
  // The pre-paint script in index.html already set the class; this only mirrors the stored choice.
  const [pref, setPref] = useState(getStoredTheme)
  const [osDark, setOsDark] = useState(systemPrefersDark)
  const resolved = resolveTheme(pref, osDark)

  // Track the OS scheme; in "system" mode an OS change re-applies the theme without reload.
  useEffect(() => watchSystemTheme(setOsDark), [])

  useEffect(() => {
    applyTheme(pref)
  }, [pref, osDark])

  useEffect(() => {
    // Other tabs follow a change made here.
    const onStorage = (event) => {
      if (event.key === THEME_STORAGE_KEY) setPref(getStoredTheme())
    }
    window.addEventListener("storage", onStorage)
    return () => window.removeEventListener("storage", onStorage)
  }, [])

  const choose = (value) => {
    setStoredTheme(value)
    setPref(value)
  }

  const { label, Icon } = OPTIONS[pref]
  const name = pref === "system" ? `Theme: System (${resolved})` : `Theme: ${label}`

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          aria-label={name}
          title={name}
          className="text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-offset-2 focus-visible:ring-offset-background"
        >
          <Icon className="h-4 w-4" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>Theme</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={pref} onValueChange={choose}>
          {THEMES.map((value) => {
            const { label: itemLabel, Icon: ItemIcon } = OPTIONS[value]
            return (
              <DropdownMenuRadioItem key={value} value={value}>
                <ItemIcon className="h-4 w-4" aria-hidden="true" />
                {itemLabel}
              </DropdownMenuRadioItem>
            )
          })}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
