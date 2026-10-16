import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { AuthProvider } from './context/AuthContext'
import { UIProvider } from './context/UIContext'
import App from './App'

// Self-hosted so the dashboard looks right on a host with no internet,
// which is where an in-process ops tool usually runs. Latin subset,
// weights 400/500/600, woff2 only — about 120KB in all.
import './fonts.css'

import './tokens.css'
import './index.css'
import './app.css'
import './ops.css'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <BrowserRouter>
      <AuthProvider>
        <UIProvider>
          <App />
        </UIProvider>
      </AuthProvider>
    </BrowserRouter>
  </StrictMode>
)
