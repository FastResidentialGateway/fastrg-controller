import React, { useCallback, useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import IconButton from '@mui/material/IconButton'
import Stack from '@mui/material/Stack'
import TextField from '@mui/material/TextField'
import Tooltip from '@mui/material/Tooltip'
import Typography from '@mui/material/Typography'
import AddOutlinedIcon from '@mui/icons-material/AddOutlined'
import DeleteOutlinedIcon from '@mui/icons-material/DeleteOutlined'
import { getHSIConfig, updateHSIConfig } from '../../api'
import LabeledField from '../../components/LabeledField'
import { useNotify } from '../../components/NotifyProvider'
import { useI18n } from '../../i18n/I18nContext'
import SectionCard from './SectionCard'
import UserSelect from './UserSelect'
import { USER_ID_EXCEEDS, baseConfigPayload, extractApiError, unwrapConfig } from './utils'
import { validatePortMappings } from './validation'

const MONO_INPUT = { '& input': { fontFamily: '"JetBrains Mono", monospace', fontSize: 12.5 } }

export default function SnatTab({ nodeId, userIds, selectedUserId, onSelectUser }) {
  const { t } = useI18n()
  const { notify } = useNotify()
  const [portMappings, setPortMappings] = useState([])
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState(null)

  const loadConfig = useCallback(async (userId) => {
    setLoading(true)
    setError(null)
    try {
      const configData = unwrapConfig(await getHSIConfig(nodeId, userId))
      setPortMappings(Array.isArray(configData['port-mapping']) ? configData['port-mapping'] : [])
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.loadConfigFailed')
      if (msg === USER_ID_EXCEEDS) notify(t('hsi.error.userIdExceeds') || msg, { severity: 'error' })
      else setError(msg)
    } finally {
      setLoading(false)
    }
  }, [nodeId, notify, t])

  useEffect(() => {
    if (selectedUserId) loadConfig(selectedUserId)
    else setPortMappings([])
  }, [selectedUserId, loadConfig])

  const updateRow = (index, field, value) => {
    setPortMappings(prev => prev.map((pm, i) => i === index ? { ...pm, [field]: value } : pm))
  }

  const handleSave = async () => {
    const portMappingError = validatePortMappings(portMappings, t)
    if (portMappingError) {
      notify(portMappingError, { severity: 'warning', duration: 5000 })
      return
    }

    setSaving(true)
    setError(null)
    try {
      // Read the stored config first so the write keeps every other field.
      const configData = unwrapConfig(await getHSIConfig(nodeId, selectedUserId))
      const fullConfig = baseConfigPayload(configData, selectedUserId)

      if (portMappings.length > 0) {
        fullConfig['port-mapping'] = portMappings.map((pm, idx) => ({
          index: String(idx),
          dip: pm.dip,
          dport: pm.dport,
          eport: pm.eport
        }))
      }

      await updateHSIConfig(nodeId, selectedUserId, fullConfig)
      notify(t('hsi.saveSuccess'), { severity: 'success' })
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.saveFailed')
      if (msg === USER_ID_EXCEEDS) notify(t('hsi.error.userIdExceeds') || msg, { severity: 'error' })
      else setError(msg)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Stack spacing={3}>
      {error && <Alert severity="error">{error}</Alert>}

      <SectionCard
        title={t('hsi.snatPortForwarding')}
        subtitle={t('hsi.portMappingHint')}
        sx={{ maxWidth: 900, borderTop: 0, pt: 0 }}
      >
        <UserSelect userIds={userIds} value={selectedUserId} onChange={onSelectUser} />

        {selectedUserId && (
          <Stack spacing={1.5} sx={{ mt: 2.5 }}>
            {portMappings.map((pm, idx) => (
              <Stack
                key={idx}
                direction={{ xs: 'column', sm: 'row' }}
                spacing={1.5}
                sx={{ alignItems: 'flex-end', borderTop: 1, borderColor: 'divider', pt: 1.5 }}
              >
                <Typography variant="caption" color="text.disabled" sx={{ minWidth: 24, pb: 1 }}>#{idx + 1}</Typography>
                <LabeledField label={t('hsi.portMapping.dip')} width={200}>
                  {({ id }) => (
                    <TextField
                      id={id}
                      placeholder={t('hsi.portMapping.dipPlaceholder')}
                      value={pm.dip}
                      onChange={(e) => updateRow(idx, 'dip', e.target.value)}
                      sx={MONO_INPUT}
                      fullWidth
                    />
                  )}
                </LabeledField>
                <LabeledField label={t('hsi.portMapping.dport')} width={130}>
                  {({ id }) => (
                    <TextField
                      id={id}
                      placeholder={t('hsi.portMapping.dportPlaceholder')}
                      value={pm.dport}
                      onChange={(e) => updateRow(idx, 'dport', e.target.value)}
                      sx={MONO_INPUT}
                      fullWidth
                    />
                  )}
                </LabeledField>
                <LabeledField label={t('hsi.portMapping.eport')} width={130}>
                  {({ id }) => (
                    <TextField
                      id={id}
                      placeholder={t('hsi.portMapping.eportPlaceholder')}
                      value={pm.eport}
                      onChange={(e) => updateRow(idx, 'eport', e.target.value)}
                      sx={MONO_INPUT}
                      fullWidth
                    />
                  )}
                </LabeledField>
                <Tooltip title={t('common.delete')}>
                  <IconButton
                    color="error"
                    aria-label={t('common.delete')}
                    onClick={() => setPortMappings(prev => prev.filter((_, i) => i !== idx))}
                  >
                    <DeleteOutlinedIcon fontSize="small" />
                  </IconButton>
                </Tooltip>
              </Stack>
            ))}
            <Stack direction="row" spacing={1}>
              <Button
                variant="outlined"
                startIcon={<AddOutlinedIcon />}
                onClick={() => setPortMappings(prev => [...prev, { dip: '', dport: '', eport: '' }])}
                disabled={loading}
              >
                {t('hsi.portMapping.addRule')}
              </Button>
              <Button variant="contained" onClick={handleSave} disabled={saving || loading}>
                {saving ? t('common.processing') : t('hsi.confirm')}
              </Button>
            </Stack>
          </Stack>
        )}
      </SectionCard>
    </Stack>
  )
}
