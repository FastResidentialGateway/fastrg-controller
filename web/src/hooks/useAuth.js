import { useState, useEffect } from 'react'

export function useAuth() {
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [isLoading, setIsLoading] = useState(true)

  useEffect(() => {
    // Initial auth check
    const checkAuth = () => {
      const token = localStorage.getItem('token')
      setIsAuthenticated(!!token)
      setIsLoading(false)
    }

    checkAuth()

    // Listen for storage changes (cross-tab synchronization)
    const handleStorageChange = (e) => {
      if (e.key === 'token') {
        const hasToken = !!e.newValue
        setIsAuthenticated(hasToken)

        // If token was removed, redirect to login if not already there
        if (!hasToken && window.location.pathname !== '/') {
          window.location.href = '/'
        }
      }
    }

    // Listen for custom logout events from API interceptor
    const handleAuthLogout = (e) => {
      // suppress debug logging; update auth state
      setIsAuthenticated(false)
    }

    window.addEventListener('storage', handleStorageChange)
    window.addEventListener('auth:logout', handleAuthLogout)
    
    return () => {
      window.removeEventListener('storage', handleStorageChange)
      window.removeEventListener('auth:logout', handleAuthLogout)
    }
  }, [])

  const login = (token) => {
    localStorage.setItem('token', token)
    setIsAuthenticated(true)
  }

  const logout = () => {
    localStorage.removeItem('token')
    setIsAuthenticated(false)
  }

  return {
    isAuthenticated,
    isLoading,
    login,
    logout
  }
}
