import React, { useCallback, useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Checkbox from '@mui/material/Checkbox'
import FormControlLabel from '@mui/material/FormControlLabel'
import MenuItem from '@mui/material/MenuItem'
import Skeleton from '@mui/material/Skeleton'
import Stack from '@mui/material/Stack'
import Switch from '@mui/material/Switch'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableContainer from '@mui/material/TableContainer'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import { getAllFailedEvents, deleteFailedEvents } from '../api'
import { PageActions } from '../components/AppShell'
import { useConfirm } from '../components/ConfirmProvider'
import LabeledField from '../components/LabeledField'
import { useNotify } from '../components/NotifyProvider'
import { useI18n } from '../i18n/I18nContext'

const REFRESH_INTERVAL_MS = 10000
const MONO_STACK = '"JetBrains Mono", ui-monospace, monospace'

const EVENT_TYPES = [
  { value: 'CONFIG_APPLY_FAIL', label: 'CONFIG_APPLY_FAIL' },
  { value: 'CONFIG_APPLY_OK', label: 'CONFIG_APPLY_OK' },
  { value: 'RUNTIME_ERROR', label: 'RUNTIME_ERROR' },
]

// Severity drives the 2px bar on the left edge of a row and the type color.
const SEVERITY = {
  CONFIG_APPLY_OK: 'success',
  CONFIG_APPLY_FAIL: 'error',
  RUNTIME_ERROR: 'error',
}

function formatTimestamp(eventTime) {
  if (!eventTime) return ''
  const date = new Date(eventTime)
  if (isNaN(date.getTime())) return eventTime
  const pad = n => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

export default function FailedEvents() {
  const { t } = useI18n()
  const confirm = useConfirm()
  const { notify } = useNotify()
  const [events, setEvents] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [eventTypeFilter, setEventTypeFilter] = useState('')
  const [selectedIds, setSelectedIds] = useState(new Set())
  const [deleting, setDeleting] = useState(false)

  const fetchEvents = useCallback(async () => {
    try {
      const data = await getAllFailedEvents(eventTypeFilter || null)
      setEvents(data)
      setError(null)
      // Drop selections for events that no longer exist.
      setSelectedIds(prev => {
        const existing = new Set(data.map(e => e.id))
        return new Set([...prev].filter(id => existing.has(id)))
      })
    } catch (err) {
      setError(err.message || 'Failed to fetch events')
    } finally {
      setLoading(false)
    }
  }, [eventTypeFilter])

  useEffect(() => {
    fetchEvents()

    let interval
    if (autoRefresh) {
      interval = setInterval(fetchEvents, REFRESH_INTERVAL_MS)
    }

    return () => {
      if (interval) clearInterval(interval)
    }
  }, [autoRefresh, fetchEvents])

  const allIds = events.map(e => e.id)
  const allSelected = allIds.length > 0 && allIds.every(id => selectedIds.has(id))
  const someSelected = allIds.some(id => selectedIds.has(id))

  const toggleSelectAll = () => {
    setSelectedIds(allSelected ? new Set() : new Set(allIds))
  }

  const toggleSelect = (id) => {
    setSelectedIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const handleDeleteSelected = async () => {
    const ids = [...selectedIds]
    if (ids.length === 0) return

    const accepted = await confirm({
      title: t('events.deleteSelected'),
      message: t('events.confirmDelete').replace('{count}', ids.length),
      confirmText: t('common.delete'),
      destructive: true,
    })
    if (!accepted) return

    setDeleting(true)
    try {
      const result = await deleteFailedEvents(ids)
      const deleted = result.deleted ?? ids.length
      notify(t('events.deleteSuccess').replace('{count}', deleted), { severity: 'success' })
      setSelectedIds(new Set())
      await fetchEvents()
    } catch (err) {
      notify(`${t('events.deleteFailed')}: ${err.message || ''}`, { severity: 'error', duration: 6000 })
    } finally {
      setDeleting(false)
    }
  }

  return (
    <Stack spacing={2}>
      <PageActions>
        <Typography variant="caption" color="text.secondary">
          {t('events.count').replace('{count}', events.length)}
        </Typography>
      </PageActions>

      <Stack direction="row" spacing={1.5} sx={{ alignItems: 'flex-start', flexWrap: 'wrap' }}>
        <LabeledField label={t('events.filterByType')} width={220}>
          {({ id, labelId }) => (
            <TextField
              id={id}
              select
              value={eventTypeFilter}
              onChange={(e) => setEventTypeFilter(e.target.value)}
              slotProps={{ select: { displayEmpty: true, labelId } }}
              fullWidth
            >
              <MenuItem value="">{t('events.allTypes')}</MenuItem>
              {EVENT_TYPES.map(type => (
                <MenuItem key={type.value} value={type.value} sx={{ fontFamily: MONO_STACK, fontSize: 12.5 }}>
                  {type.label}
                </MenuItem>
              ))}
            </TextField>
          )}
        </LabeledField>
        <FormControlLabel
          sx={{ mt: 2 }}
          control={<Switch checked={autoRefresh} onChange={(e) => setAutoRefresh(e.target.checked)} />}
          label={<Typography variant="caption">{t('events.autoRefresh')} 10s</Typography>}
        />
        <Button variant="outlined" sx={{ mt: 2 }} onClick={fetchEvents} disabled={loading}>
          {t('common.refresh')}
        </Button>
        <Box sx={{ width: '1px', height: 20, mt: 2, bgcolor: 'divider' }} />
        <Button color="error" sx={{ mt: 2 }} onClick={handleDeleteSelected} disabled={!someSelected || deleting}>
          {deleting ? t('common.processing') : `${t('events.deleteSelected')} (${selectedIds.size})`}
        </Button>
      </Stack>

      {error && <Alert severity="error">{t('common.error')}: {error}</Alert>}

      {loading && events.length === 0 ? (
        <Skeleton variant="rectangular" height={240} />
      ) : events.length === 0 ? (
        <Box sx={{ py: 8, textAlign: 'center' }}>
          <Typography variant="body2" color="text.secondary">{t('events.noEvents')}</Typography>
        </Box>
      ) : (
        <Box sx={{ borderTop: 1, borderColor: 'divider' }}>
          <TableContainer sx={{ maxHeight: '70vh' }}>
            <Table stickyHeader sx={{ minWidth: 1000 }}>
              <TableHead>
                <TableRow>
                  <TableCell padding="checkbox">
                    <Checkbox
                      checked={allSelected}
                      indeterminate={someSelected && !allSelected}
                      onChange={toggleSelectAll}
                      inputProps={{ 'aria-label': t('events.selectAll') }}
                    />
                  </TableCell>
                  <TableCell>{t('events.timestamp')}</TableCell>
                  <TableCell>{t('events.type')}</TableCell>
                  <TableCell>{t('events.node')}</TableCell>
                  <TableCell>{t('events.userId')}</TableCell>
                  <TableCell>{t('events.moduleAction')}</TableCell>
                  <TableCell>{t('events.errorCode')}</TableCell>
                  <TableCell>{t('events.errorMessage')}</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {events.map((event, index) => {
                  const isSelected = selectedIds.has(event.id)
                  const severity = SEVERITY[event.event_type]
                  const severityColor = severity ? `${severity}.main` : 'text.disabled'
                  return (
                    <TableRow key={event.id ?? index} hover selected={isSelected}>
                      <TableCell
                        padding="checkbox"
                        sx={{
                          boxShadow: (theme) => `inset 2px 0 0 ${severity ? theme.palette[severity].main : theme.palette.text.disabled}`,
                        }}
                      >
                        <Checkbox checked={isSelected} onChange={() => toggleSelect(event.id)} />
                      </TableCell>
                      <TableCell><Box component="code" sx={{ color: 'text.secondary', whiteSpace: 'nowrap' }}>{formatTimestamp(event.event_time)}</Box></TableCell>
                      <TableCell><Box component="code" sx={{ color: severityColor }}>{event.event_type}</Box></TableCell>
                      <TableCell><Box component="code">{event.node_uuid}</Box></TableCell>
                      <TableCell><Box component="code">{event.user_id}</Box></TableCell>
                      <TableCell sx={{ color: 'text.secondary' }}>{event.module || event.action || '—'}</TableCell>
                      <TableCell>
                        {event.error_code
                          ? <Box component="code" sx={{ color: 'error.main' }}>{event.error_code}</Box>
                          : <Box component="span" sx={{ color: 'text.disabled' }}>—</Box>}
                      </TableCell>
                      <TableCell sx={{ maxWidth: 420, color: 'text.secondary', overflowWrap: 'anywhere' }}>
                        {event.error_message || '—'}
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          </TableContainer>
        </Box>
      )}
    </Stack>
  )
}
