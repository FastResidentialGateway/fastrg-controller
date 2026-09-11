import React, { useCallback, useEffect, useRef, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Checkbox from '@mui/material/Checkbox'
import Collapse from '@mui/material/Collapse'
import FormControlLabel from '@mui/material/FormControlLabel'
import IconButton from '@mui/material/IconButton'
import Stack from '@mui/material/Stack'
import TableCell from '@mui/material/TableCell'
import TableRow from '@mui/material/TableRow'
import TextField from '@mui/material/TextField'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import CallEndOutlinedIcon from '@mui/icons-material/CallEndOutlined'
import CallOutlinedIcon from '@mui/icons-material/CallOutlined'
import DeleteOutlinedIcon from '@mui/icons-material/DeleteOutlined'
import ExpandLessOutlinedIcon from '@mui/icons-material/ExpandLessOutlined'
import ExpandMoreOutlinedIcon from '@mui/icons-material/ExpandMoreOutlined'
import VisibilityOffOutlinedIcon from '@mui/icons-material/VisibilityOffOutlined'
import VisibilityOutlinedIcon from '@mui/icons-material/VisibilityOutlined'
import {
  createHSIConfig,
  deleteHSIConfig,
  dialPPPoE,
  getHSIConfig,
  getHSIUserIds,
  getPPPoEInfo,
  getPPPoEStatus,
  hangupPPPoE,
  updateHSIConfig
} from '../../api'
import { useConfirm } from '../../components/ConfirmProvider'
import LabeledField from '../../components/LabeledField'
import { useNotify } from '../../components/NotifyProvider'
import StatusDot from '../../components/StatusDot'
import { useI18n } from '../../i18n/I18nContext'
import DataTable from './DataTable'
import Mono from './Mono'
import RevealPasswordDialog from './RevealPasswordDialog'
import SectionCard from './SectionCard'
import { useIpv6RedialConfirm } from './useIpv6RedialConfirm'
import { USER_ID_EXCEEDS, extractApiError, unwrapConfig } from './utils'
import { validateDHCPConfig, validatePPPoEConfig } from './validation'

const EMPTY_PPPOE = {
  user_id: '',
  vlan_id: '',
  account_name: '',
  password: '',
  ipv6_enable: false,
  dns_proxy_enable: true,
  tcp_conntrack_enable: true,
  // desireStatus is the PPPoE expected state from config: "connect" | "disconnect"
  desireStatus: ''
}

const EMPTY_DHCP = { dhcp_addr_pool: '', dhcp_subnet: '', dhcp_gateway: '' }

const MONO_INPUT = { '& input': { fontFamily: '"JetBrains Mono", monospace', fontSize: 12.5 } }

export default function PppoeTab({ nodeId, onUsersChanged }) {
  const { t } = useI18n()
  const confirm = useConfirm()
  const { notify } = useNotify()
  const confirmIpv6Redial = useIpv6RedialConfirm()

  const [step, setStep] = useState(0)
  const [pppoeConfig, setPppoeConfig] = useState(EMPTY_PPPOE)
  const [dhcpConfig, setDhcpConfig] = useState(EMPTY_DHCP)
  const [loadedIpv6Enable, setLoadedIpv6Enable] = useState(null)
  const [isUpdate, setIsUpdate] = useState(false)
  const [isCheckingConfig, setIsCheckingConfig] = useState(false)
  const [touchedFields, setTouchedFields] = useState({})
  const [fieldErrors, setFieldErrors] = useState({})

  const [configs, setConfigs] = useState([])
  const [panelLoading, setPanelLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState(null)

  const [revealedPasswords, setRevealedPasswords] = useState({})
  const [revealFor, setRevealFor] = useState(null)
  const [infoMap, setInfoMap] = useState({})

  const autoFillTimer = useRef(null)
  useEffect(() => () => clearTimeout(autoFillTimer.current), [])

  const reportError = useCallback((err, fallbackKey, asNotify) => {
    const msg = extractApiError(err) || t(fallbackKey)
    if (msg === USER_ID_EXCEEDS) notify(t('hsi.error.userIdExceeds') || msg, { severity: 'error' })
    else if (asNotify) notify(msg, { severity: 'error', duration: 5000 })
    else setError(msg)
  }, [notify, t])

  const loadConfigs = useCallback(async () => {
    setPanelLoading(true)
    setError(null)
    try {
      const ids = await getHSIUserIds(nodeId)
      if (onUsersChanged) onUsersChanged(ids)
      const loaded = await Promise.all(ids.map(async (uid) => {
        try {
          const configData = unwrapConfig(await getHSIConfig(nodeId, uid))
          return {
            user_id: configData.user_id || uid,
            vlan_id: configData.vlan_id || '',
            account_name: configData.account_name || '',
            password: configData.password || '',
            desireStatus: configData.desire_status || ''
          }
        } catch (_) {
          return { user_id: uid, vlan_id: '', account_name: '', password: '', desireStatus: '' }
        }
      }))
      setConfigs(loaded)
    } catch (err) {
      reportError(err, 'hsi.loadUserIdsFailed')
    } finally {
      setPanelLoading(false)
    }
  }, [nodeId, onUsersChanged, reportError])

  useEffect(() => {
    loadConfigs()
  }, [nodeId])

  // Typing an existing user id pulls that config into the form for updating.
  const silentAutoFillConfig = async (userId) => {
    if (!userId) return
    setIsCheckingConfig(true)
    try {
      const configData = unwrapConfig(await getHSIConfig(nodeId, userId))
      setPppoeConfig(prev => ({
        ...prev,
        vlan_id: configData.vlan_id || '',
        account_name: configData.account_name || '',
        password: configData.password || '',
        ipv6_enable: configData.ipv6_enable !== undefined ? configData.ipv6_enable : false,
        dns_proxy_enable: configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true,
        tcp_conntrack_enable: configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true,
        desireStatus: configData.desire_status || ''
      }))
      setLoadedIpv6Enable(configData.ipv6_enable !== undefined ? configData.ipv6_enable : false)
      setDhcpConfig({
        dhcp_addr_pool: configData.dhcp_addr_pool || '',
        dhcp_subnet: configData.dhcp_subnet || '',
        dhcp_gateway: configData.dhcp_gateway || ''
      })
      setIsUpdate(true)
    } catch (_) {
      setIsUpdate(false)
      setLoadedIpv6Enable(null)
    } finally {
      setIsCheckingConfig(false)
    }
  }

  const handleInputChange = (field, value) => {
    if (step === 0) {
      setPppoeConfig(prev => ({ ...prev, [field]: value }))
      if (field === 'user_id') {
        clearTimeout(autoFillTimer.current)
        if (value.trim() === '') {
          setIsCheckingConfig(false)
          setLoadedIpv6Enable(null)
          setIsUpdate(false)
          return
        }
        autoFillTimer.current = setTimeout(() => silentAutoFillConfig(value.trim()), 500)
      }
    } else {
      setDhcpConfig(prev => ({ ...prev, [field]: value }))
    }

    if (typeof value !== 'string' || value.trim() !== '') {
      setFieldErrors(prev => ({ ...prev, [field]: false }))
    }
  }

  const handleFieldFocus = (field) => {
    setTouchedFields(prev => ({ ...prev, [field]: true }))
  }

  const handleFieldBlur = (field) => {
    const currentValue = step === 0 ? pppoeConfig[field] : dhcpConfig[field]
    if (touchedFields[field] && (!currentValue || currentValue.trim() === '')) {
      setFieldErrors(prev => ({ ...prev, [field]: true }))
    }
  }

  // Each field carries its own width so the form reads as a form, not a stack
  // of full-width boxes.
  const formField = (field, label, width, extra = {}) => (
    <LabeledField label={label} width={width}>
      {({ id }) => (
        <TextField
          id={id}
          value={(step === 0 ? pppoeConfig : dhcpConfig)[field] ?? ''}
          onChange={(e) => handleInputChange(field, e.target.value)}
          onFocus={() => handleFieldFocus(field)}
          onBlur={() => handleFieldBlur(field)}
          error={fieldErrors[field] === true}
          helperText={fieldErrors[field] === true ? t('common.fieldRequired') : undefined}
          sx={MONO_INPUT}
          fullWidth
          {...extra}
        />
      )}
    </LabeledField>
  )

  const resetForm = () => {
    setStep(0)
    setPppoeConfig(EMPTY_PPPOE)
    setDhcpConfig(EMPTY_DHCP)
    setLoadedIpv6Enable(null)
    setTouchedFields({})
    setFieldErrors({})
  }

  const handleSubmit = async () => {
    if (step === 0) {
      const validationError = validatePPPoEConfig(pppoeConfig, t)
      if (validationError) {
        notify(validationError, { severity: 'warning', duration: 5000 })
        return
      }
      setStep(1)
      setTouchedFields({})
      setFieldErrors({})
      return
    }

    const dhcpValidationError = validateDHCPConfig(dhcpConfig, t)
    if (dhcpValidationError) {
      notify(dhcpValidationError, { severity: 'warning', duration: 5000 })
      return
    }

    setSaving(true)
    setError(null)
    try {
      // Saving rewrites ipv6_enable, so ask only when the form flips the value
      // the config was loaded with.
      if (loadedIpv6Enable !== null && pppoeConfig.ipv6_enable !== loadedIpv6Enable) {
        const confirmed = await confirmIpv6Redial(nodeId, pppoeConfig.user_id)
        if (!confirmed) return
      }

      let exists = false
      try {
        await getHSIConfig(nodeId, pppoeConfig.user_id)
        exists = true
      } catch (_) {
        exists = false
      }

      // Only HSIConfig fields go out; desire_status stays with the backend.
      const fullConfig = {
        user_id: pppoeConfig.user_id,
        vlan_id: pppoeConfig.vlan_id,
        account_name: pppoeConfig.account_name,
        password: pppoeConfig.password,
        ipv6_enable: pppoeConfig.ipv6_enable,
        dns_proxy_enable: pppoeConfig.dns_proxy_enable,
        tcp_conntrack_enable: pppoeConfig.tcp_conntrack_enable,
        dhcp_addr_pool: dhcpConfig.dhcp_addr_pool,
        dhcp_subnet: dhcpConfig.dhcp_subnet,
        dhcp_gateway: dhcpConfig.dhcp_gateway
      }

      if (exists) await updateHSIConfig(nodeId, pppoeConfig.user_id, fullConfig)
      else await createHSIConfig(nodeId, fullConfig)
      notify(t('hsi.saveSuccess'), { severity: 'success' })

      resetForm()
      setIsUpdate(false)
      await loadConfigs()
    } catch (err) {
      reportError(err, 'hsi.saveFailed')
    } finally {
      setSaving(false)
    }
  }

  const rowAction = async (userId, { confirmKey, run, successKey, failKey, destructive }) => {
    const accepted = await confirm({
      message: t(confirmKey).replace('{userId}', userId),
      destructive
    })
    if (!accepted) return
    setSaving(true)
    setError(null)
    try {
      await run()
      notify(t(successKey), { severity: 'success' })
      await loadConfigs()
    } catch (err) {
      reportError(err, failKey, true)
    } finally {
      setSaving(false)
    }
  }

  const handleDeleteRow = (userId) => rowAction(userId, {
    confirmKey: 'hsi.confirmDelete',
    run: () => deleteHSIConfig(nodeId, userId),
    successKey: 'hsi.deleteSuccess',
    failKey: 'hsi.deleteFailed',
    destructive: true
  })

  const handleDialRow = (userId) => rowAction(userId, {
    confirmKey: 'hsi.confirmDial',
    run: () => dialPPPoE(nodeId, userId),
    successKey: 'hsi.dialSuccess',
    failKey: 'hsi.dialFailed'
  })

  const handleHangupRow = (userId) => rowAction(userId, {
    confirmKey: 'hsi.confirmHangup',
    run: () => hangupPPPoE(nodeId, userId),
    successKey: 'hsi.hangupSuccess',
    failKey: 'hsi.hangupFailed'
  })

  // Live session data from the node, plus the recorded Kafka-fed status.
  const handleToggleInfo = async (userId) => {
    const current = infoMap[userId]
    if (current && current.expanded) {
      setInfoMap(prev => ({ ...prev, [userId]: { ...prev[userId], expanded: false } }))
      return
    }
    if (current && current.data) {
      setInfoMap(prev => ({ ...prev, [userId]: { ...prev[userId], expanded: true } }))
      return
    }

    setInfoMap(prev => ({ ...prev, [userId]: { loading: true, data: null, recorded: null, error: null, expanded: true } }))
    const [infoResult, statusResult] = await Promise.allSettled([
      getPPPoEInfo(nodeId, userId),
      getPPPoEStatus(nodeId, userId)
    ])
    const recorded = statusResult.status === 'fulfilled' ? statusResult.value : null
    if (infoResult.status === 'fulfilled') {
      setInfoMap(prev => ({ ...prev, [userId]: { loading: false, data: infoResult.value, recorded, error: null, expanded: true } }))
    } else {
      const msg = extractApiError(infoResult.reason) || t('hsi.pppoeInfoNotAvailable')
      setInfoMap(prev => ({ ...prev, [userId]: { loading: false, data: null, recorded, error: msg, expanded: true } }))
    }
  }

  const hidePassword = (userId) => setRevealedPasswords(prev => {
    const next = { ...prev }
    delete next[userId]
    return next
  })

  const columns = [
    { label: t('hsi.userId') },
    { label: t('hsi.vlanLabel') },
    { label: t('hsi.accountNameLabel') },
    { label: t('hsi.password') },
    { label: t('hsi.status') },
    { label: t('hsi.actions'), align: 'right' }
  ]

  return (
    <Stack spacing={3}>
      {error && <Alert severity="error">{error}</Alert>}

      <SectionCard
        title={isUpdate ? t('hsi.updatePppoeConfig') : t('hsi.createPppoe')}
        subtitle={t('hsi.pppoeHint')}
        action={isCheckingConfig
          ? <Typography variant="caption" color="primary">{t('hsi.checkingConfig')}</Typography>
          : null}
        sx={{ maxWidth: 720, borderTop: 0, pt: 0 }}
      >
        <Stack direction="row" spacing={3} sx={{ mb: 2.5, borderBottom: 1, borderColor: 'divider' }}>
          {[t('hsi.pppoeConfig'), t('hsi.dhcpSettings')].map((label, i) => (
            <Box
              key={label}
              sx={{ pb: 1, mb: '-1px', borderBottom: 2, borderColor: i === step ? 'primary.main' : 'transparent' }}
            >
              <Typography variant="caption" sx={{ color: i === step ? 'text.primary' : 'text.disabled' }}>
                {i + 1}. {label}
              </Typography>
            </Box>
          ))}
        </Stack>

        {isUpdate && step === 0 && (
          <Alert severity="warning" sx={{ mb: 2 }}>{t('hsi.pppoeUpdateNotice')}</Alert>
        )}

        {step === 0 ? (
          <Stack spacing={2}>
            <Stack direction="row" spacing={1.5} sx={{ flexWrap: 'wrap' }}>
              {formField('user_id', `${t('hsi.userId')} (1-2000)`, 130)}
              {formField('vlan_id', `${t('hsi.vlanLabel')} (2-4000)`, 130)}
            </Stack>
            <Stack direction="row" spacing={1.5} sx={{ flexWrap: 'wrap' }}>
              {formField('account_name', t('hsi.accountNameLabel'), 260)}
              {formField('password', t('hsi.password'), 200, { type: 'password' })}
            </Stack>
            <FormControlLabel
              control={
                <Checkbox
                  checked={pppoeConfig.ipv6_enable}
                  onChange={(e) => handleInputChange('ipv6_enable', e.target.checked)}
                />
              }
              label={t('hsi.ipv6EnableLabel')}
            />
            <Box>
              <Button variant="contained" onClick={handleSubmit} disabled={saving}>
                {saving ? t('common.processing') : t('hsi.nextStepDhcp')}
              </Button>
            </Box>
          </Stack>
        ) : (
          <Stack spacing={2}>
            <Stack direction="row" spacing={1.5} sx={{ flexWrap: 'wrap' }}>
              {formField('dhcp_addr_pool', t('hsi.dhcpAddrPoolLabel'), 280, { placeholder: t('hsi.example.dhcpPool') })}
              {formField('dhcp_subnet', t('hsi.subnetLabel'), 180, { placeholder: t('hsi.example.subnet') })}
              {formField('dhcp_gateway', t('hsi.gatewayLabel'), 180, { placeholder: t('hsi.example.gateway') })}
            </Stack>
            <Stack direction="row" spacing={1}>
              <Button
                color="inherit"
                onClick={() => { setStep(0); setTouchedFields({}); setFieldErrors({}) }}
              >
                {t('common.back')}
              </Button>
              <Button variant="contained" onClick={handleSubmit} disabled={saving}>
                {saving ? t('common.processing') : t('hsi.confirm')}
              </Button>
            </Stack>
          </Stack>
        )}
      </SectionCard>

      <SectionCard title={t('hsi.currentPppoeConfigs')}>
        <DataTable
          columns={columns}
          loading={panelLoading}
          isEmpty={configs.length === 0}
          emptyText={t('hsi.noPppoeConfigs')}
          minWidth={800}
        >
          {configs.map((cfg) => {
            const connected = (cfg.desireStatus || '').toLowerCase() === 'connect'
            const info = infoMap[cfg.user_id]
            return (
              <React.Fragment key={cfg.user_id}>
                <TableRow hover>
                  <TableCell><Mono>{cfg.user_id}</Mono></TableCell>
                  <TableCell><Mono>{cfg.vlan_id}</Mono></TableCell>
                  <TableCell>
                    <Tooltip title={cfg.account_name || ''}>
                      {/* The width sits here: a cell's own max-width is ignored. */}
                      <Box sx={{ maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        <Mono>{cfg.account_name}</Mono>
                      </Box>
                    </Tooltip>
                  </TableCell>
                  <TableCell sx={{ whiteSpace: 'nowrap' }}>
                    {revealedPasswords[cfg.user_id] ? (
                      <>
                        <Box component="code" sx={{ mr: 0.5 }}>{cfg.password}</Box>
                        <Tooltip title={t('hsi.hidePassword')}>
                          <IconButton onClick={() => hidePassword(cfg.user_id)} aria-label={t('hsi.hidePassword')}>
                            <VisibilityOffOutlinedIcon fontSize="small" />
                          </IconButton>
                        </Tooltip>
                      </>
                    ) : (
                      <>
                        <Box component="span" sx={{ letterSpacing: 2, mr: 0.5 }}>••••</Box>
                        <Tooltip title={t('hsi.revealPassword')}>
                          <IconButton onClick={() => setRevealFor(cfg.user_id)} aria-label={t('hsi.revealPassword')}>
                            <VisibilityOutlinedIcon fontSize="small" />
                          </IconButton>
                        </Tooltip>
                      </>
                    )}
                  </TableCell>
                  <TableCell>
                    <StatusDot
                      color={connected ? 'success.main' : 'text.disabled'}
                      label={connected ? t('hsi.statusOn') : t('hsi.statusOff')}
                      dim={!connected}
                    />
                  </TableCell>
                  <TableCell align="right">
                    <Stack direction="row" spacing={0.5} sx={{ justifyContent: 'flex-end' }}>
                      <Tooltip title={t('hsi.dialAction')}>
                        <span>
                          <IconButton onClick={() => handleDialRow(cfg.user_id)} disabled={saving} aria-label={t('hsi.dialAction')}>
                            <CallOutlinedIcon fontSize="small" />
                          </IconButton>
                        </span>
                      </Tooltip>
                      <Tooltip title={t('hsi.hangupAction')}>
                        <span>
                          <IconButton onClick={() => handleHangupRow(cfg.user_id)} disabled={saving} aria-label={t('hsi.hangupAction')}>
                            <CallEndOutlinedIcon fontSize="small" />
                          </IconButton>
                        </span>
                      </Tooltip>
                      <Tooltip title={info && info.expanded ? t('hsi.hidePPPoEInfo') : t('hsi.showPPPoEInfo')}>
                        <span>
                          <IconButton
                            onClick={() => handleToggleInfo(cfg.user_id)}
                            disabled={Boolean(info && info.loading)}
                            aria-label={t('hsi.showPPPoEInfo')}
                          >
                            {info && info.expanded
                              ? <ExpandLessOutlinedIcon fontSize="small" />
                              : <ExpandMoreOutlinedIcon fontSize="small" />}
                          </IconButton>
                        </span>
                      </Tooltip>
                      <Tooltip title={t('hsi.deleteAction')}>
                        <span>
                          <IconButton color="error" onClick={() => handleDeleteRow(cfg.user_id)} disabled={saving} aria-label={t('hsi.deleteAction')}>
                            <DeleteOutlinedIcon fontSize="small" />
                          </IconButton>
                        </span>
                      </Tooltip>
                    </Stack>
                  </TableCell>
                </TableRow>
                <TableRow>
                  <TableCell colSpan={columns.length} sx={{ py: 0, border: 0 }}>
                    <Collapse in={Boolean(info && info.expanded && !info.loading)} unmountOnExit>
                      <PppoeInfoPanel userId={cfg.user_id} info={info} />
                    </Collapse>
                  </TableCell>
                </TableRow>
              </React.Fragment>
            )
          })}
        </DataTable>
      </SectionCard>

      <RevealPasswordDialog
        open={revealFor !== null}
        userId={revealFor}
        onClose={() => setRevealFor(null)}
        onVerified={(userId) => setRevealedPasswords(prev => ({ ...prev, [userId]: true }))}
      />
    </Stack>
  )
}

function PppoeInfoPanel({ userId, info }) {
  const { t } = useI18n()
  if (!info) return null
  if (info.error) {
    return <Alert severity="error" sx={{ my: 1 }}>{info.error}</Alert>
  }

  const notSet = t('common.notSet')
  const dnsServers = info.data?.dns_servers
  const rows = [
    [t('hsi.pppoeSessionId'), info.data?.session_id || notSet],
    [t('hsi.pppoeStatus'), info.data?.status || notSet],
    [t('hsi.pppoeClientIp'), info.data?.client_ip || notSet],
    [t('hsi.pppoeServerIp'), info.data?.server_ip || notSet],
    [t('hsi.pppoeDnsServers'), dnsServers && dnsServers.length > 0 ? dnsServers.join(', ') : notSet],
    [t('hsi.pppoeIpv6Addr'), info.recorded?.hsi_ipv6 || notSet],
    [t('hsi.pppoeIpv6PdPrefix'), info.recorded?.hsi_ipv6_pd_prefix || notSet],
    [t('hsi.pppoeIpv6Dns'), info.recorded?.hsi_ipv6_dns || notSet]
  ]

  return (
    <Box sx={{ my: 1, p: 2, borderLeft: 2, borderColor: 'primary.main' }}>
      <Typography variant="subtitle2" sx={{ mb: 1 }}>
        {t('hsi.pppoeInfo')} — {t('hsi.user')} {userId}
      </Typography>
      <Box sx={{ display: 'grid', gap: 1, gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, 1fr)' } }}>
        {rows.map(([label, value]) => (
          <Stack key={label} direction="row" spacing={1}>
            <Typography variant="caption" color="text.secondary" sx={{ minWidth: 150 }}>{label}</Typography>
            <Mono nowrap={false}>{value}</Mono>
          </Stack>
        ))}
      </Box>
    </Box>
  )
}
