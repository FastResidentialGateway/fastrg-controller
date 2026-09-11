import React, { useCallback, useEffect, useRef, useState } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import IconButton from '@mui/material/IconButton'
import Stack from '@mui/material/Stack'
import Switch from '@mui/material/Switch'
import TableCell from '@mui/material/TableCell'
import TableRow from '@mui/material/TableRow'
import TextField from '@mui/material/TextField'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import DeleteOutlinedIcon from '@mui/icons-material/DeleteOutlined'
import {
  addOrUpdateDnsRecord,
  deleteDnsRecord,
  getDnsRecord,
  getDnsRecords,
  getHSIConfig,
  updateHSIConfig
} from '../../api'
import { useConfirm } from '../../components/ConfirmProvider'
import LabeledField from '../../components/LabeledField'
import { useNotify } from '../../components/NotifyProvider'
import StatusDot from '../../components/StatusDot'
import { useI18n } from '../../i18n/I18nContext'
import DataTable from './DataTable'
import Mono from './Mono'
import SectionCard from './SectionCard'
import UserSelect from './UserSelect'
import { baseConfigPayload, extractApiError, unwrapConfig, withPortMapping } from './utils'
import { validateDnsRecord } from './validation'

const MONO_INPUT = { '& input': { fontFamily: '"JetBrains Mono", monospace', fontSize: 12.5 } }

export default function DnsTab({ nodeId, userIds, selectedUserId, onSelectUser }) {
  const { t } = useI18n()
  const confirm = useConfirm()
  const { notify } = useNotify()

  const [records, setRecords] = useState([])
  const [form, setForm] = useState({ domain: '', ip: '', ttl: '' })
  const [isUpdate, setIsUpdate] = useState(false)
  const [checkingDomain, setCheckingDomain] = useState(false)
  const [recordsLoading, setRecordsLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [proxyEnable, setProxyEnable] = useState(null)
  const [proxyLoading, setProxyLoading] = useState(false)

  const domainTimer = useRef(null)
  useEffect(() => () => clearTimeout(domainTimer.current), [])

  const loadRecords = useCallback(async (userId) => {
    setRecordsLoading(true)
    setRecords([])
    setForm({ domain: '', ip: '', ttl: '' })
    setIsUpdate(false)
    try {
      setRecords(await getDnsRecords(nodeId, userId))
    } catch (err) {
      // 404 just means this subscriber has no records yet.
      if (!err.response || err.response.status !== 404) {
        notify(extractApiError(err) || t('dns.loadFailed'), { severity: 'error' })
      }
      setRecords([])
    } finally {
      setRecordsLoading(false)
    }
  }, [nodeId, notify, t])

  const loadProxyEnable = useCallback(async (userId) => {
    setProxyLoading(true)
    setProxyEnable(null)
    try {
      const configData = unwrapConfig(await getHSIConfig(nodeId, userId))
      setProxyEnable(configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true)
    } catch (_) {
      setProxyEnable(true)
    } finally {
      setProxyLoading(false)
    }
  }, [nodeId])

  useEffect(() => {
    if (!selectedUserId) {
      setRecords([])
      setProxyEnable(null)
      return
    }
    loadRecords(selectedUserId)
    loadProxyEnable(selectedUserId)
  }, [selectedUserId, loadRecords, loadProxyEnable])

  const handleToggleProxy = async () => {
    if (!selectedUserId || proxyEnable === null) return
    setProxyLoading(true)
    try {
      const configData = unwrapConfig(await getHSIConfig(nodeId, selectedUserId))
      const newValue = !proxyEnable
      const fullConfig = withPortMapping(
        { ...baseConfigPayload(configData, selectedUserId), dns_proxy_enable: newValue },
        configData
      )
      await updateHSIConfig(nodeId, selectedUserId, fullConfig)
      setProxyEnable(newValue)
      notify(t('hsi.saveSuccess'), { severity: 'success' })
    } catch (err) {
      notify(extractApiError(err) || t('hsi.saveFailed'), { severity: 'error' })
    } finally {
      setProxyLoading(false)
    }
  }

  // Typing a domain that already has a record switches the form to update mode.
  const handleDomainChange = (value) => {
    setForm(prev => ({ ...prev, domain: value }))
    setIsUpdate(false)
    clearTimeout(domainTimer.current)

    if (value.trim() === '' || !selectedUserId) {
      setCheckingDomain(false)
      return
    }

    domainTimer.current = setTimeout(async () => {
      setCheckingDomain(true)
      try {
        const record = await getDnsRecord(nodeId, selectedUserId, value.trim())
        setForm(prev => ({
          ...prev,
          ip: record.ip || '',
          ttl: record.ttl !== undefined ? String(record.ttl) : ''
        }))
        setIsUpdate(true)
      } catch (_) {
        setIsUpdate(false)
      } finally {
        setCheckingDomain(false)
      }
    }, 500)
  }

  const handleSubmit = async () => {
    const validationError = validateDnsRecord(form, t)
    if (validationError) {
      notify(validationError, { severity: 'warning', duration: 5000 })
      return
    }

    setSaving(true)
    try {
      const result = await addOrUpdateDnsRecord(nodeId, selectedUserId, {
        domain: form.domain.trim(),
        ip: form.ip.trim(),
        ttl: parseInt(form.ttl, 10)
      })
      notify(result.action === 'updated' ? t('dns.updateSuccess') : t('dns.addSuccess'), { severity: 'success' })
      setForm({ domain: '', ip: '', ttl: '' })
      setIsUpdate(false)
      await loadRecords(selectedUserId)
    } catch (err) {
      const isMaxRecordsError = err.response && err.response.status === 422
      const msg = isMaxRecordsError ? t('dns.error.maxRecords') : (extractApiError(err) || t('dns.saveFailed'))
      notify(msg, { severity: 'error', duration: 5000 })
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async (domain) => {
    const accepted = await confirm({
      title: t('common.delete'),
      message: t('dns.confirmDelete').replace('{domain}', domain),
      confirmText: t('common.delete'),
      destructive: true
    })
    if (!accepted) return

    setSaving(true)
    try {
      await deleteDnsRecord(nodeId, selectedUserId, domain)
      notify(t('dns.deleteSuccess'), { severity: 'success' })
      await loadRecords(selectedUserId)
    } catch (err) {
      notify(extractApiError(err) || t('dns.deleteFailed'), { severity: 'error', duration: 5000 })
    } finally {
      setSaving(false)
    }
  }

  return (
    <Stack spacing={3}>
      <SectionCard
        title={t('dns.staticDnsRecord')}
        subtitle={t('dns.hint')}
        sx={{ maxWidth: 900, borderTop: 0, pt: 0 }}
      >
        <UserSelect userIds={userIds} value={selectedUserId} onChange={onSelectUser} />
      </SectionCard>

      {selectedUserId && (
        <>
          <SectionCard title={t('hsi.dnsProxy')} sx={{ maxWidth: 900 }}>
            {proxyEnable === null && proxyLoading ? (
              <Typography variant="caption" color="text.secondary">{t('common.loading')}</Typography>
            ) : proxyEnable !== null ? (
              <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
                <Switch checked={proxyEnable} onChange={handleToggleProxy} disabled={proxyLoading} />
                <StatusDot
                  color={proxyEnable ? 'success.main' : 'text.disabled'}
                  label={proxyEnable ? t('hsi.dnsProxyEnabled') : t('hsi.dnsProxyDisabled')}
                  dim={!proxyEnable}
                />
              </Stack>
            ) : null}
          </SectionCard>

          <SectionCard
            title={isUpdate ? t('dns.updateRecord') : t('dns.addRecord')}
            subtitle={isUpdate ? undefined : t('dns.maxRecordsHint')}
            action={checkingDomain
              ? <Typography variant="caption" color="primary">{t('dns.checkingDomain')}</Typography>
              : null}
            sx={{ maxWidth: 900 }}
          >
            {isUpdate && <Alert severity="warning" sx={{ mb: 2 }}>{t('dns.updateNotice')}</Alert>}
            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1.5} sx={{ alignItems: 'flex-end' }}>
              <LabeledField label={t('dns.domain')} width={280}>
                {({ id }) => (
                  <TextField
                    id={id}
                    placeholder={t('dns.domainPlaceholder')}
                    value={form.domain}
                    onChange={(e) => handleDomainChange(e.target.value)}
                    sx={MONO_INPUT}
                    fullWidth
                  />
                )}
              </LabeledField>
              <LabeledField label={t('dns.ip')} width={180}>
                {({ id }) => (
                  <TextField
                    id={id}
                    placeholder={t('dns.ipPlaceholder')}
                    value={form.ip}
                    onChange={(e) => setForm(prev => ({ ...prev, ip: e.target.value }))}
                    sx={MONO_INPUT}
                    fullWidth
                  />
                )}
              </LabeledField>
              <LabeledField label={t('dns.ttl')} width={110}>
                {({ id }) => (
                  <TextField
                    id={id}
                    type="number"
                    placeholder={t('dns.ttlPlaceholder')}
                    slotProps={{ htmlInput: { min: 1 } }}
                    value={form.ttl}
                    onChange={(e) => setForm(prev => ({ ...prev, ttl: e.target.value }))}
                    sx={MONO_INPUT}
                    fullWidth
                  />
                )}
              </LabeledField>
              <Button
                variant="contained"
                onClick={handleSubmit}
                disabled={saving || checkingDomain}
                sx={{ whiteSpace: 'nowrap' }}
              >
                {saving ? t('common.processing') : (isUpdate ? t('dns.update') : t('dns.add'))}
              </Button>
            </Stack>
          </SectionCard>

          <SectionCard title={t('dns.currentRecords')} sx={{ maxWidth: 900 }}>
            <DataTable
              columns={[
                { label: t('dns.domain') },
                { label: t('dns.ip') },
                { label: t('dns.ttl') },
                { label: t('hsi.actions'), align: 'right' }
              ]}
              loading={recordsLoading}
              isEmpty={records.length === 0}
              emptyText={t('dns.noRecords')}
            >
              {records.map((rec, idx) => (
                <TableRow key={idx} hover>
                  <TableCell><Mono>{rec.domain}</Mono></TableCell>
                  <TableCell><Mono>{rec.ip}</Mono></TableCell>
                  <TableCell><Mono dim>{rec.ttl}</Mono></TableCell>
                  <TableCell align="right">
                    <Tooltip title={t('common.delete')}>
                      <span>
                        <IconButton
                          color="error"
                          aria-label={t('common.delete')}
                          onClick={() => handleDelete(rec.domain)}
                          disabled={saving}
                        >
                          <DeleteOutlinedIcon fontSize="small" />
                        </IconButton>
                      </span>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ))}
            </DataTable>
          </SectionCard>
        </>
      )}
    </Stack>
  )
}
