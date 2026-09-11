import React from 'react'
import { Routes, Route, useNavigate } from 'react-router-dom'
import Box from '@mui/material/Box'
import CircularProgress from '@mui/material/CircularProgress'
import Login from './pages/Login'
import Nodes from './pages/Nodes'
import HSIConfigPage from './pages/hsi/HSIConfigPage'
import FailedEvents from './pages/FailedEvents'
import AppShell from './components/AppShell'
import ProtectedRoute from './components/ProtectedRoute'
import { useAuth } from './hooks/useAuth'

export default function App(){
  const { isAuthenticated, isLoading, login, logout } = useAuth()
  const navigate = useNavigate()

  function handleLogout() {
    logout()
    navigate('/')
  }

  // Wait for the stored token to be read before rendering any route.
  if (isLoading) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', pt: 10 }}>
        <CircularProgress size={28} />
      </Box>
    )
  }

  return (
    <AppShell isAuthenticated={isAuthenticated} onLogout={handleLogout}>
      <Routes>
        <Route path="/" element={<Login onLogin={login} />} />
        <Route path="/nodes" element={
          <ProtectedRoute>
            <Nodes/>
          </ProtectedRoute>
        } />
        <Route path="/nodes/:nodeId/hsi" element={
          <ProtectedRoute>
            <HSIConfigPage/>
          </ProtectedRoute>
        } />
        <Route path="/failed-events" element={
          <ProtectedRoute>
            <FailedEvents/>
          </ProtectedRoute>
        } />
      </Routes>
    </AppShell>
  )
}
