import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { consumeTokenFragment } from './api/client'
import { applyTheme, watchSystemTheme } from './lib/theme'
import './index.css'
import App from './App.tsx'

consumeTokenFragment()
applyTheme()
watchSystemTheme()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)
