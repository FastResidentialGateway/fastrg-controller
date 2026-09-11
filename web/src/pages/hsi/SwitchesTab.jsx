import React, { useCallback, useEffect, useState } from 'react'
import Box from '@mui/material/Box'
import Divider from '@mui/material/Divider'
import Stack from '@mui/material/Stack'
import Switch from '@mui/material/Switch'
import Typography from '@mui/material/Typography'
import { getHSIConfig, updateHSIConfig } from '../../api'
import { useNotify } from '../../components/NotifyProvider'
import StatusDot from '../../components/StatusDot'
import { useI18n } from '../../i18n/I18nContext'
import SectionCard from './SectionCard'
import UserSelect from './UserSelect'
import { useIpv6RedialConfirm } from './useIpv6RedialConfirm'
import { baseConfigPayload, extractApiError, unwrapConfig, withPortMapping } from './utils'

// One switch row: label on the left, control and state dot on the right.
function ToggleRow({ label, checked, loading, onChange, onLabel, offLabel }) {
  const { t } = useI18n()
  return (
    <Stack direction="row" spacing={1.5} sx={{ alignItems: 'center', py: 1 }}>
      <Typography variant="body2" sx={{ flexGrow: 1 }}>{label}</Typography>
      {checked === null ? (
        <Typography variant="caption" color="text.secondary">{t('common.loading')}</Typography>
      ) : (
        <>
          <Switch checked={checked} onChange={onChange} disabled={loading} />
          <Box sx={{ minWidth: 52 }}>
            <StatusDot
              color={checked ? 'success.main' : 'text.disabled'}
              label={checked ? onLabel : offLabel}
              dim={!checked}
            />
          </Box>
        </>
      )}
    </Stack>
  )
}

export default function SwitchesTab({ nodeId, userIds, selectedUserId, onSelectUser }) {
  const { t } = useI18n()
  const { notify } = useNotify()
  const confirmIpv6Redial = useIpv6RedialConfirm()
  const [tcpConntrackEnable, setTcpConntrackEnable] = useState(null)
  const [ipv6Enable, setIpv6Enable] = useState(null)
  const [loading, setLoading] = useState(false)

  const loadConfig = useCallback(async (userId) => {
    setLoading(true)
    setTcpConntrackEnable(null)
    setIpv6Enable(null)
    try {
      const configData = unwrapConfig(await getHSIConfig(nodeId, userId))
      setTcpConntrackEnable(configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true)
      setIpv6Enable(configData.ipv6_enable !== undefined ? configData.ipv6_enable : false)
    } catch (_) {
      setTcpConntrackEnable(true)
      setIpv6Enable(false)
    } finally {
      setLoading(false)
    }
  }, [nodeId])

  useEffect(() => {
    if (!selectedUserId) {
      setTcpConntrackEnable(null)
      setIpv6Enable(null)
      return
    }
    loadConfig(selectedUserId)
  }, [selectedUserId, loadConfig])

  const handleToggleTcpConntrack = async () => {
    if (!selectedUserId || tcpConntrackEnable === null) return
    setLoading(true)
    try {
      const configData = unwrapConfig(await getHSIConfig(nodeId, selectedUserId))
      const newValue = !tcpConntrackEnable
      const fullConfig = withPortMapping(
        { ...baseConfigPayload(configData, selectedUserId), tcp_conntrack_enable: newValue },
        configData
      )
      await updateHSIConfig(nodeId, selectedUserId, fullConfig)
      setTcpConntrackEnable(newValue)
      notify(t('hsi.saveSuccess'), { severity: 'success' })
    } catch (err) {
      notify(extractApiError(err) || t('hsi.saveFailed'), { severity: 'error' })
    } finally {
      setLoading(false)
    }
  }

  const handleToggleIpv6 = async () => {
    if (!selectedUserId || ipv6Enable === null) return
    setLoading(true)
    try {
      const confirmed = await confirmIpv6Redial(nodeId, selectedUserId)
      if (!confirmed) return

      const configData = unwrapConfig(await getHSIConfig(nodeId, selectedUserId))
      const newValue = !ipv6Enable
      const fullConfig = withPortMapping(
        { ...baseConfigPayload(configData, selectedUserId), ipv6_enable: newValue },
        configData
      )
      await updateHSIConfig(nodeId, selectedUserId, fullConfig)
      setIpv6Enable(newValue)
      notify(t('hsi.saveSuccess'), { severity: 'success' })
    } catch (err) {
      notify(extractApiError(err) || t('hsi.saveFailed'), { severity: 'error' })
    } finally {
      setLoading(false)
    }
  }

  return (
    <Stack spacing={3}>
      <SectionCard title={t('hsi.otherSwitches')} sx={{ maxWidth: 520, borderTop: 0, pt: 0 }}>
        <UserSelect userIds={userIds} value={selectedUserId} onChange={onSelectUser} />

        {selectedUserId && (
          <Stack sx={{ mt: 2 }} divider={<Divider />}>
            <ToggleRow
              label={t('hsi.tcpConntrack')}
              checked={tcpConntrackEnable}
              loading={loading}
              onChange={handleToggleTcpConntrack}
              onLabel={t('hsi.tcpConntrackEnabled')}
              offLabel={t('hsi.tcpConntrackDisabled')}
            />
            <ToggleRow
              label={t('hsi.ipv6Toggle')}
              checked={ipv6Enable}
              loading={loading}
              onChange={handleToggleIpv6}
              onLabel={t('hsi.ipv6Enabled')}
              offLabel={t('hsi.ipv6Disabled')}
            />
          </Stack>
        )}
      </SectionCard>
    </Stack>
  )
}
