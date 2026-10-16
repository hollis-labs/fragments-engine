import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import { AuthGate } from '@/components/AuthGate'
import { ApiProvider } from '@/contexts/ApiContext'
import { applyTheme, getInitialTheme } from '@hollis-labs/sysop-ui'

applyTheme(getInitialTheme())

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <AuthGate>
      <ApiProvider>
        <App />
      </ApiProvider>
    </AuthGate>
  </StrictMode>,
)
