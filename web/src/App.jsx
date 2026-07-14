import React from 'react'
import { Routes, Route, Link, useNavigate } from 'react-router-dom'
import Login from './pages/Login'
import Nodes from './pages/Nodes'
import HSIConfig from './pages/HSIConfig'
import FailedEvents from './pages/FailedEvents'
import ProtectedRoute from './components/ProtectedRoute'
import { useAuth } from './hooks/useAuth'
import { useI18n } from './i18n/I18nContext'

export default function App(){
  const { isAuthenticated, isLoading, login, logout } = useAuth()
  const { t } = useI18n()
  const navigate = useNavigate()

  // Logout functionality
  function handleLogout() {
    logout()
    navigate('/')
  }

  // Show loading state while checking authentication
  if (isLoading) {
    return <div className="loading">{t('common.loading')}</div>
  }

  return (
    <div className="app">
      <header className="app-header">
        <h1>{t('app.title')}</h1>
        <nav>
          {isAuthenticated ? (
            <>
              <Link to="/nodes">{t('nav.nodes')}</Link> | 
              <Link to="/failed-events">{t('nav.failedEvents')}</Link> | 
              <button onClick={handleLogout} className="logout-btn">{t('nav.logout')}</button>
            </>
          ) : (
            <Link to="/">{t('nav.login')}</Link>
          )}
        </nav>
      </header>
      <main>
        <Routes>
          <Route path="/" element={<Login onLogin={login} />} />
          <Route path="/nodes" element={
            <ProtectedRoute>
              <Nodes/>
            </ProtectedRoute>
          } />
          <Route path="/nodes/:nodeId/hsi" element={
            <ProtectedRoute>
              <HSIConfig/>
            </ProtectedRoute>
          } />
          <Route path="/failed-events" element={
            <ProtectedRoute>
              <FailedEvents/>
            </ProtectedRoute>
          } />
        </Routes>
      </main>
    </div>
  )
}
