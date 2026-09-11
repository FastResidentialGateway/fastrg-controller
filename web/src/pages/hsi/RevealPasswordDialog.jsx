import React, { useState } from 'react'
import Button from '@mui/material/Button'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogContentText from '@mui/material/DialogContentText'
import DialogTitle from '@mui/material/DialogTitle'
import TextField from '@mui/material/TextField'
import { verifyAdminPassword } from '../../api'
import LabeledField from '../../components/LabeledField'
import { useI18n } from '../../i18n/I18nContext'

// Asks for the admin password before a subscriber's dial password is shown.
export default function RevealPasswordDialog({ open, userId, onClose, onVerified }) {
  const { t } = useI18n()
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')

  const close = () => {
    setPassword('')
    setLoading(false)
    setError('')
    onClose()
  }

  const submit = async () => {
    if (!password) return
    setLoading(true)
    setError('')
    try {
      await verifyAdminPassword(password)
      onVerified(userId)
      close()
    } catch (err) {
      setLoading(false)
      setError(t('hsi.revealPasswordModal.wrongPassword'))
    }
  }

  return (
    <Dialog open={open} onClose={close} maxWidth="xs" fullWidth>
      <DialogTitle>{t('hsi.revealPasswordModal.title')}</DialogTitle>
      <DialogContent>
        <DialogContentText sx={{ mb: 2 }}>
          {t('hsi.revealPasswordModal.hint')}
          {userId && <strong> (User ID: {userId})</strong>}
        </DialogContentText>
        <LabeledField label={t('hsi.revealPasswordModal.adminPasswordLabel')} width={240}>
          {({ id }) => (
            <TextField
              id={id}
              type="password"
              value={password}
              onChange={(e) => { setPassword(e.target.value); setError('') }}
              onKeyDown={(e) => { if (e.key === 'Enter') submit() }}
              error={Boolean(error)}
              helperText={error || undefined}
              autoFocus
              fullWidth
            />
          )}
        </LabeledField>
      </DialogContent>
      <DialogActions sx={{ px: 3, pb: 2 }}>
        <Button onClick={close} color="inherit">{t('hsi.revealPasswordModal.cancel')}</Button>
        <Button onClick={submit} variant="contained" disabled={loading || !password}>
          {loading ? t('common.processing') : t('hsi.revealPasswordModal.submit')}
        </Button>
      </DialogActions>
    </Dialog>
  )
}
