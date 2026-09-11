import React, { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Stack from '@mui/material/Stack'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import HubOutlinedIcon from '@mui/icons-material/HubOutlined'
import { apiLogin, SESSION_EXPIRED_NOTICE } from '../api'
import { useNotify } from '../components/NotifyProvider'
import { useI18n } from '../i18n/I18nContext'

export default function Login({ onLogin }){
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState(null)
  const [submitting, setSubmitting] = useState(false)
  const { t } = useI18n()
  const { notify } = useNotify()
  const navigate = useNavigate()

  // The auth interceptor redirects here after a token expires; report it once.
  useEffect(() => {
    if (sessionStorage.getItem(SESSION_EXPIRED_NOTICE)) {
      sessionStorage.removeItem(SESSION_EXPIRED_NOTICE)
      notify(t('login.sessionExpired'), { severity: 'warning', duration: 6000 })
    }
  }, [notify, t])

  async function submit(e){
    e.preventDefault()
    setError(null)
    setSubmitting(true)
    try{
      const token = await apiLogin(username, password)
      if (onLogin) onLogin(token)
      navigate('/nodes')
    }catch(err){
      if (err.response && err.response.status === 401) {
        setError(t('login.invalidCredentials'))
      } else {
        setError(err.message || t('login.networkError'))
      }
    }finally{
      setSubmitting(false)
    }
  }

  return (
    <Box sx={{ minHeight: '100vh', display: 'flex', flexDirection: 'column', px: 4, py: 3 }}>
      <Box sx={{ pt: '16vh', pl: { xs: 0, md: 6 } }}>
        <form onSubmit={submit} noValidate>
          <Stack spacing={2} sx={{ width: 360, maxWidth: '100%' }}>
            {/* The product name leads the column; the page has no other brand.
                The row starts flush with the fields, so the icon's left edge is
                the column's alignment edge. */}
            <Stack direction="row" spacing={1.5} sx={{ alignItems: 'center' }}>
              <HubOutlinedIcon sx={{ fontSize: 36, color: 'primary.main' }} />
              <Typography
                component="h1"
                sx={{ fontSize: 36, fontWeight: 600, letterSpacing: '-0.02em', lineHeight: 1.1 }}
              >
                {t('app.title')}
              </Typography>
            </Stack>
            <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
              {t('login.title')}
            </Typography>
            <TextField
              label={t('login.username')}
              value={username}
              onChange={e => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus
              fullWidth
            />
            <TextField
              label={t('login.password')}
              type="password"
              value={password}
              onChange={e => setPassword(e.target.value)}
              autoComplete="current-password"
              fullWidth
            />
            {error && <Alert severity="error">{error}</Alert>}
            <Box>
              <Button type="submit" variant="contained" disabled={submitting}>
                {submitting ? t('common.processing') : t('login.button')}
              </Button>
            </Box>
          </Stack>
        </form>
      </Box>
    </Box>
  )
}
