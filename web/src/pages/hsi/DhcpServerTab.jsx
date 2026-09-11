import React, { useCallback, useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Skeleton from '@mui/material/Skeleton'
import Stack from '@mui/material/Stack'
import Typography from '@mui/material/Typography'
import RefreshOutlinedIcon from '@mui/icons-material/RefreshOutlined'
import { getDhcpConfig } from '../../api'
import { useI18n } from '../../i18n/I18nContext'
import Mono from './Mono'
import SectionCard from './SectionCard'
import UserSelect from './UserSelect'
import { extractApiError } from './utils'

function Field({ label, children, wide }) {
  return (
    <Box sx={{ gridColumn: wide ? '1 / -1' : 'auto' }}>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>{label}</Typography>
      <Typography variant="body2" sx={{ wordBreak: 'break-word' }}>{children}</Typography>
    </Box>
  )
}

export default function DhcpServerTab({ nodeId, userIds, selectedUserId, onSelectUser }) {
  const { t } = useI18n()
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const load = useCallback(async (userId) => {
    setLoading(true)
    setData(null)
    setError(null)
    try {
      setData(await getDhcpConfig(nodeId, userId))
    } catch (err) {
      setError(extractApiError(err) || t('hsi.dhcpConfigNotAvailable'))
    } finally {
      setLoading(false)
    }
  }, [nodeId, t])

  useEffect(() => {
    if (!selectedUserId) {
      setData(null)
      setError(null)
      return
    }
    load(selectedUserId)
  }, [selectedUserId, load])

  const notSet = t('common.notSet')
  const usagePercent = data && data.max_lease_count > 0
    ? ` (${Math.round((data.cur_lease_count / data.max_lease_count) * 100)}%)`
    : ''

  return (
    <Stack spacing={3}>
      <SectionCard
        title={t('hsi.dhcpServer')}
        subtitle={t('hsi.dhcpServerHint')}
        action={selectedUserId ? (
          <Button
            variant="outlined"
            startIcon={<RefreshOutlinedIcon />}
            onClick={() => load(selectedUserId)}
            disabled={loading}
          >
            {t('hsi.refreshDhcpConfig')}
          </Button>
        ) : null}
      >
        <UserSelect userIds={userIds} value={selectedUserId} onChange={onSelectUser} />
      </SectionCard>

      {selectedUserId && (
        <>
          {error && <Alert severity="error">{error}</Alert>}
          {!error && (
            <SectionCard
              title={`${t('hsi.dhcpConfig')} — ${t('hsi.user')} ${selectedUserId}`}
              sx={{ maxWidth: 720 }}
            >
              {loading ? (
                <Skeleton variant="rounded" height={140} />
              ) : data ? (
                <Box sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', sm: 'repeat(2, 1fr)' } }}>
                  <Field label={t('hsi.dhcpStatus')}>{data.status || notSet}</Field>
                  <Field label={t('hsi.dhcpAddrPoolLabel')}><Mono>{data.ip_range || notSet}</Mono></Field>
                  <Field label={t('hsi.subnetLabel')}><Mono>{data.subnet_mask || notSet}</Mono></Field>
                  <Field label={t('hsi.gatewayLabel')}><Mono>{data.gateway || notSet}</Mono></Field>
                  <Field label={t('hsi.dhcpLeaseUsage')} wide>
                    <Mono>{data.cur_lease_count} / {data.max_lease_count}{usagePercent}</Mono>
                  </Field>
                  {data.inuse_ips && data.inuse_ips.length > 0 && (
                    <Field label={t('hsi.dhcpInuseIpsLabel')} wide>
                      <Mono nowrap={false}>{data.inuse_ips.join(', ')}</Mono>
                    </Field>
                  )}
                </Box>
              ) : null}
            </SectionCard>
          )}
        </>
      )}
    </Stack>
  )
}
