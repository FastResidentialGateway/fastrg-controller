import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
import Alert from '@mui/material/Alert'
import Snackbar from '@mui/material/Snackbar'
import Stack from '@mui/material/Stack'

const NotifyContext = createContext(null)

const PLACEMENTS = ['top', 'bottom']

// Application-wide snackbar queue: notify() returns an id so a caller can take
// its own message down early, and each message expires on its own timer.
export function NotifyProvider({ children }) {
  const [items, setItems] = useState([])
  const nextId = useRef(1)
  const timers = useRef(new Map())

  const dismiss = useCallback((id) => {
    const timer = timers.current.get(id)
    if (timer) {
      clearTimeout(timer)
      timers.current.delete(id)
    }
    setItems(prev => prev.filter(item => item.id !== id))
  }, [])

  const dismissAll = useCallback(() => {
    timers.current.forEach(clearTimeout)
    timers.current.clear()
    setItems([])
  }, [])

  const notify = useCallback((message, options = {}) => {
    const { severity = 'info', duration = 4000, placement = 'bottom' } = options
    const id = nextId.current++
    setItems(prev => [...prev, { id, message, severity, placement }])
    if (duration > 0) {
      timers.current.set(id, setTimeout(() => dismiss(id), duration))
    }
    return id
  }, [dismiss])

  useEffect(() => () => {
    timers.current.forEach(clearTimeout)
    timers.current.clear()
  }, [])

  const value = useMemo(() => ({ notify, dismiss, dismissAll }), [notify, dismiss, dismissAll])

  return (
    <NotifyContext.Provider value={value}>
      {children}
      {PLACEMENTS.map(placement => {
        const shown = items.filter(item => item.placement === placement)
        return (
          <Snackbar
            key={placement}
            open={shown.length > 0}
            anchorOrigin={{ vertical: placement, horizontal: 'right' }}
            sx={{ maxWidth: 440 }}
          >
            <Stack spacing={1} sx={{ width: '100%' }}>
              {shown.map(item => (
                <Alert
                  key={item.id}
                  severity={item.severity}
                  icon={false}
                  onClose={() => dismiss(item.id)}
                  sx={{
                    alignItems: 'center',
                    wordBreak: 'break-word',
                    bgcolor: 'background.paper',
                    border: 1,
                    borderColor: 'divider',
                    borderLeft: 2,
                    borderLeftColor: `${item.severity}.main`,
                    color: 'text.primary',
                  }}
                >
                  {item.message}
                </Alert>
              ))}
            </Stack>
          </Snackbar>
        )
      })}
    </NotifyContext.Provider>
  )
}

export function useNotify() {
  const context = useContext(NotifyContext)
  if (!context) {
    throw new Error('useNotify must be used within NotifyProvider')
  }
  return context
}

export default useNotify
