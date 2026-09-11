import React, { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import CircularProgress from '@mui/material/CircularProgress'
import Collapse from '@mui/material/Collapse'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import IconButton from '@mui/material/IconButton'
import Stack from '@mui/material/Stack'
import TableCell from '@mui/material/TableCell'
import TableRow from '@mui/material/TableRow'
import TextField from '@mui/material/TextField'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import ContentCopyOutlinedIcon from '@mui/icons-material/ContentCopyOutlined'
import KeyboardArrowDownOutlinedIcon from '@mui/icons-material/KeyboardArrowDownOutlined'
import KeyboardArrowUpOutlinedIcon from '@mui/icons-material/KeyboardArrowUpOutlined'
import { apiUnregisterNode, getNodeSubscriberCount, updateNodeSubscriberCount } from '../api'
import { useI18n } from '../i18n/I18nContext'
import { useConfirm } from './ConfirmProvider'
import LabeledField from './LabeledField'
import { useNotify } from './NotifyProvider'
import StatusDot from './StatusDot'

export const NODE_COLUMNS = 9

function Mono({ children, dim, nowrap = true }) {
  return (
    <Box
      component="code"
      sx={{
        color: dim ? 'text.secondary' : 'text.primary',
        whiteSpace: nowrap ? 'nowrap' : 'normal',
        overflowWrap: nowrap ? 'normal' : 'anywhere',
      }}
    >
      {children}
    </Box>
  )
}

// One-line cell content capped at a pixel width: anything longer is cut with
// an ellipsis and the full text stays available in the tooltip. The width sits
// on this inner box because a table cell's own max-width is not honoured.
function Truncated({ title, width, children }) {
  return (
    <Tooltip title={title}>
      <Box sx={{ maxWidth: width, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {children}
      </Box>
    </Tooltip>
  )
}

function DetailField({ label, children, wide }) {
  return (
    <Box sx={{ gridColumn: wide ? '1 / -1' : 'auto', minWidth: 0 }}>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>{label}</Typography>
      <Box sx={{ fontSize: 12.5, overflowWrap: 'anywhere' }}>{children}</Box>
    </Box>
  )
}

function formatEpoch(seconds) {
  const d = new Date(Number(seconds) * 1000)
  const pad = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

export default function NodeRow({ node, onNodeUnregistered }) {
  const navigate = useNavigate()
  const { t } = useI18n()
  const confirm = useConfirm()
  const { notify } = useNotify()
  const [open, setOpen] = useState(false)
  const [showSubscriberModal, setShowSubscriberModal] = useState(false)
  const [subscriberCount, setSubscriberCount] = useState('')
  const [currentSubscriberCount, setCurrentSubscriberCount] = useState('')
  const [loadingSubscriberCount, setLoadingSubscriberCount] = useState(false)

  // The nodes endpoint may hand back raw etcd key/value pairs.
  let nodeData = node
  if (typeof node.value === 'string') {
    try {
      nodeData = JSON.parse(node.value)
    } catch (e) {
      nodeData = { node_uuid: 'parse error' }
    }
  }

  const isInactive = nodeData.status === 'inactive'
  const nodeUuid = nodeData.uuid || nodeData.node_uuid
  const title = nodeUuid || node.key || 'unknown'
  const apiError = (error) => error?.response?.data?.error || error?.message || ''

  const requireUuid = () => {
    if (!nodeUuid) notify(t('nodes.cannotGetUuid'), { severity: 'error' })
    return nodeUuid
  }

  const copyUuid = async () => {
    try {
      await navigator.clipboard.writeText(title)
      notify(t('common.copied'), { severity: 'success', duration: 1500 })
    } catch (_) {
      // Clipboard access can be denied; the id stays selectable in the table.
    }
  }

  const handleUnregister = async () => {
    if (!requireUuid()) return
    const accepted = await confirm({
      title: t('nodes.unregister'),
      message: t('nodes.confirmUnregister').replace('{uuid}', nodeUuid),
      confirmText: t('nodes.unregister'),
      destructive: true,
    })
    if (!accepted) return

    try {
      await apiUnregisterNode(nodeUuid)
      notify(t('nodes.unregisterSuccess'), { severity: 'success' })
      if (onNodeUnregistered) onNodeUnregistered()
    } catch (error) {
      notify(`${t('nodes.unregisterFailed')}: ${apiError(error)}`, { severity: 'error', duration: 6000 })
    }
  }

  const handleConfigHSI = () => {
    if (!requireUuid()) return
    navigate(`/nodes/${nodeUuid}/hsi`)
  }

  const handleOpenSubscriberModal = async () => {
    if (!requireUuid()) return

    setLoadingSubscriberCount(true)
    setShowSubscriberModal(true)

    try {
      const data = await getNodeSubscriberCount(nodeUuid)
      const count = data.subscriber_count.toString()
      setCurrentSubscriberCount(count)
      setSubscriberCount(count)
    } catch (error) {
      // A node that never had a count set answers 404; treat it as zero.
      if (!(error.response && error.response.status === 404)) {
        notify(`${t('nodes.getSubscriberCountFailed')}: ${apiError(error)}`, { severity: 'error', duration: 6000 })
      }
      setCurrentSubscriberCount('0')
      setSubscriberCount('0')
    } finally {
      setLoadingSubscriberCount(false)
    }
  }

  const handleUpdateSubscriberCount = async () => {
    if (!requireUuid()) return

    const count = parseInt(subscriberCount, 10)
    if (isNaN(count) || count < 0) {
      notify(t('nodes.invalidSubscriberCount'), { severity: 'warning' })
      return
    }

    try {
      await updateNodeSubscriberCount(nodeUuid, count)
      notify(t('nodes.updateSubscriberCountSuccess'), { severity: 'success' })
      setShowSubscriberModal(false)
    } catch (error) {
      notify(`${t('nodes.updateSubscriberCountFailed')}: ${apiError(error)}`, { severity: 'error', duration: 6000 })
    }
  }

  const handleCloseSubscriberModal = () => {
    setShowSubscriberModal(false)
    setSubscriberCount('')
    setCurrentSubscriberCount('')
  }

  const bootTime = (nodeData.uptime != null && nodeData.last_seen_time)
    ? formatEpoch(Number(nodeData.last_seen_time) - Number(nodeData.uptime))
    : null

  const nicWan = nodeData.nic_model_wan
  const nicLan = nodeData.nic_model_lan
  // WAN and LAN are usually the same card, so show the model once and keep the
  // per-port breakdown in the tooltip and the expanded row.
  const nicSummary = (nicWan && nicLan && nicWan !== nicLan)
    ? `${nicWan} / ${nicLan}`
    : (nicWan || nicLan || '—')
  const nicTooltip = (nicWan || nicLan)
    ? `WAN: ${nicWan || '—'}\nLAN: ${nicLan || '—'}`
    : ''

  return (
    <>
      <TableRow
        hover
        onClick={() => setOpen(v => !v)}
        sx={{ cursor: 'pointer', '& > td': { borderBottom: open ? 0 : undefined } }}
      >
        <TableCell>
          <StatusDot
            color={isInactive ? 'text.disabled' : 'success.main'}
            label={isInactive ? t('nodes.statusInactive') : t('nodes.statusActive')}
          />
        </TableCell>
        <TableCell>
          <Stack direction="row" spacing={0.5} sx={{ alignItems: 'center' }}>
            <Mono>{title}</Mono>
            <Tooltip title={t('common.copy')}>
              <IconButton
                aria-label={t('common.copy')}
                onClick={(e) => { e.stopPropagation(); copyUuid() }}
                sx={{ color: 'text.disabled', '&:hover': { color: 'text.primary' } }}
              >
                <ContentCopyOutlinedIcon sx={{ fontSize: 13 }} />
              </IconButton>
            </Tooltip>
          </Stack>
        </TableCell>
        <TableCell>
          <Truncated title={nodeData.location || ''} width={150}>{nodeData.location || '—'}</Truncated>
        </TableCell>
        <TableCell><Mono>{nodeData.node_ip || '—'}</Mono></TableCell>
        <TableCell>
          <Truncated title={nodeData.version || ''} width={170}>
            <Mono>{nodeData.version || t('nodes.unknownVersion')}</Mono>
          </Truncated>
        </TableCell>
        <TableCell sx={{ color: 'text.secondary' }}>
          <Truncated title={<Box sx={{ whiteSpace: 'pre-line' }}>{nicTooltip}</Box>} width={200}>
            {nicSummary}
          </Truncated>
        </TableCell>
        <TableCell><Mono dim>{bootTime || '—'}</Mono></TableCell>
        <TableCell><Mono dim>{nodeData.last_seen_time ? formatEpoch(nodeData.last_seen_time) : '—'}</Mono></TableCell>
        <TableCell align="right" sx={{ width: 40 }}>
          <IconButton aria-label={open ? t('common.collapse') : t('common.expand')} sx={{ color: 'text.secondary' }}>
            {open ? <KeyboardArrowUpOutlinedIcon sx={{ fontSize: 16 }} /> : <KeyboardArrowDownOutlinedIcon sx={{ fontSize: 16 }} />}
          </IconButton>
        </TableCell>
      </TableRow>

      <TableRow>
        <TableCell colSpan={NODE_COLUMNS} sx={{ py: 0, borderBottom: open ? undefined : 0 }}>
          <Collapse in={open} unmountOnExit>
            <Stack spacing={2} sx={{ py: 2, pl: 2, borderLeft: 2, borderColor: 'primary.main', my: 1 }}>
              <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', sm: 'repeat(3, minmax(0, 1fr))' } }}>
                <DetailField label={t('nodes.hostOs')}>{nodeData.host_os || '—'}</DetailField>
                <DetailField label={`${t('nodes.nicModel')} WAN`} wide>
                  <Mono nowrap={false}>{nicWan || '—'}</Mono>
                </DetailField>
                <DetailField label={`${t('nodes.nicModel')} LAN`} wide>
                  <Mono nowrap={false}>{nicLan || '—'}</Mono>
                </DetailField>
                <DetailField label={t('nodes.registered')}>
                  <Mono dim>{nodeData.registered_at ? formatEpoch(nodeData.registered_at) : '—'}</Mono>
                </DetailField>
                <DetailField label={t('nodes.hostUptime')}><Mono dim>{bootTime || '—'}</Mono></DetailField>
                <DetailField label={t('nodes.status')}>{nodeData.status || '—'}</DetailField>
              </Box>
              <Stack direction="row" spacing={1} onClick={(e) => e.stopPropagation()}>
                <Button variant="contained" onClick={handleConfigHSI}>{t('nodes.configHSI')}</Button>
                <Button variant="outlined" onClick={handleOpenSubscriberModal}>{t('nodes.setSubscriberCount')}</Button>
                <Button color="error" onClick={handleUnregister}>{t('nodes.unregister')}</Button>
              </Stack>
            </Stack>
          </Collapse>
        </TableCell>
      </TableRow>

      <Dialog open={showSubscriberModal} onClose={handleCloseSubscriberModal} maxWidth="xs" fullWidth>
        <DialogTitle>{t('nodes.setSubscriberCount')}</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ pt: 0.5 }}>
            <Box>
              <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
                {t('nodes.currentSubscriberCount')}
              </Typography>
              {loadingSubscriberCount
                ? <CircularProgress size={14} sx={{ mt: 0.5 }} />
                : <Mono>{currentSubscriberCount || '0'}</Mono>}
            </Box>
            <LabeledField label={t('nodes.newSubscriberCountLabel')} width={160}>
              {({ id }) => (
                <TextField
                  id={id}
                  type="number"
                  slotProps={{ htmlInput: { min: 0 } }}
                  value={subscriberCount}
                  onChange={(e) => setSubscriberCount(e.target.value)}
                  disabled={loadingSubscriberCount}
                  fullWidth
                />
              )}
            </LabeledField>
          </Stack>
        </DialogContent>
        <DialogActions sx={{ px: 3, pb: 2 }}>
          <Button onClick={handleCloseSubscriberModal} color="inherit">{t('common.cancel')}</Button>
          <Button onClick={handleUpdateSubscriberCount} variant="contained" disabled={loadingSubscriberCount}>
            {t('common.save')}
          </Button>
        </DialogActions>
      </Dialog>
    </>
  )
}
