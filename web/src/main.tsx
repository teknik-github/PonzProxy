import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import { App } from "@/App"
import { AuthProvider } from "@/hooks/useAuth"
import "@/index.css"

// The console follows the operating system's light or dark preference. There
// is no in-app toggle: an operator who wants one already set it system-wide.
const dark = window.matchMedia("(prefers-color-scheme: dark)")
const applyTheme = () =>
  document.documentElement.classList.toggle("dark", dark.matches)
applyTheme()
dark.addEventListener("change", applyTheme)

const container = document.getElementById("root")
if (!container) throw new Error("the #root element is missing from index.html")

createRoot(container).render(
  <StrictMode>
    <AuthProvider>
      <App />
    </AuthProvider>
  </StrictMode>,
)
