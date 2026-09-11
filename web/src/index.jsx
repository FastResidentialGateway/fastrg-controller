import React from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import CssBaseline from '@mui/material/CssBaseline'
import { ThemeProvider } from '@mui/material/styles'
import App from './App'
import theme from './theme'
import { I18nProvider } from './i18n/I18nContext'
import { ConfirmProvider } from './components/ConfirmProvider'
import { NotifyProvider } from './components/NotifyProvider'
import ErrorBoundary from './components/ErrorBoundary'
import './styles.css'

createRoot(document.getElementById('root')).render(
  <React.StrictMode>
    <BrowserRouter>
      <I18nProvider>
        <ThemeProvider theme={theme} defaultMode="dark" modeStorageKey="fastrg-color-mode">
          <CssBaseline />
          <NotifyProvider>
            <ConfirmProvider>
              <ErrorBoundary>
                <App />
              </ErrorBoundary>
            </ConfirmProvider>
          </NotifyProvider>
        </ThemeProvider>
      </I18nProvider>
    </BrowserRouter>
  </React.StrictMode>
)
