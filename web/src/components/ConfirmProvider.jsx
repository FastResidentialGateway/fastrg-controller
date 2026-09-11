import React, { createContext, useCallback, useContext, useRef, useState } from 'react'
import Button from '@mui/material/Button'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogContentText from '@mui/material/DialogContentText'
import DialogTitle from '@mui/material/DialogTitle'
import { useI18n } from '../i18n/I18nContext'

const ConfirmContext = createContext(null)

// confirm({ title, message, confirmText, destructive }) resolves to true when
// the user accepts, false on cancel or dismissal.
export function ConfirmProvider({ children }) {
  const { t } = useI18n()
  // Options outlive `open` so the text stays put during the closing animation.
  const [open, setOpen] = useState(false)
  const [dialog, setDialog] = useState({})
  const resolver = useRef(null)

  const settle = useCallback((accepted) => {
    setOpen(false)
    if (resolver.current) {
      resolver.current(accepted)
      resolver.current = null
    }
  }, [])

  const confirm = useCallback((options) => {
    // A pending question is dropped if a new one arrives.
    if (resolver.current) resolver.current(false)
    setDialog(typeof options === 'string' ? { message: options } : (options || {}))
    setOpen(true)
    return new Promise(resolve => {
      resolver.current = resolve
    })
  }, [])

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog open={open} onClose={() => settle(false)} maxWidth="xs" fullWidth>
        <DialogTitle>{dialog.title || t('common.confirm')}</DialogTitle>
        <DialogContent>
          <DialogContentText sx={{ whiteSpace: 'pre-line' }}>{dialog.message}</DialogContentText>
        </DialogContent>
        <DialogActions sx={{ px: 3, pb: 2 }}>
          <Button onClick={() => settle(false)} color="inherit">
            {dialog.cancelText || t('common.cancel')}
          </Button>
          <Button
            onClick={() => settle(true)}
            variant="contained"
            color={dialog.destructive ? 'error' : 'primary'}
            autoFocus
          >
            {dialog.confirmText || t('common.confirm')}
          </Button>
        </DialogActions>
      </Dialog>
    </ConfirmContext.Provider>
  )
}

export function useConfirm() {
  const context = useContext(ConfirmContext)
  if (!context) {
    throw new Error('useConfirm must be used within ConfirmProvider')
  }
  return context
}

export default useConfirm
