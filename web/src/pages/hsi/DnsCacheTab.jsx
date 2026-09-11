import React, { useCallback, useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import Stack from '@mui/material/Stack'
import TableCell from '@mui/material/TableCell'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'
import RefreshOutlinedIcon from '@mui/icons-material/RefreshOutlined'
import { getDnsCache } from '../../api'
import { useI18n } from '../../i18n/I18nContext'
import DataTable from './DataTable'
import Mono from './Mono'
import SectionCard from './SectionCard'
import UserSelect from './UserSelect'
import { extractApiError } from './utils'

const MAX_ROWS = 50

export default function DnsCacheTab({ nodeId, userIds, selectedUserId, onSelectUser }) {
  const { t } = useI18n()
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const load = useCallback(async (userId) => {
    setLoading(true)
    setData(null)
    setError(null)
    try {
      setData(await getDnsCache(nodeId, userId))
    } catch (err) {
      setError(extractApiError(err) || t('hsi.dnsCacheNotAvailable'))
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

  const entries = data?.entries || []

  return (
    <Stack spacing={3}>
      <SectionCard
        title={t('hsi.dnsCache')}
        subtitle={t('hsi.dnsCacheHint')}
        action={selectedUserId ? (
          <Button
            variant="outlined"
            startIcon={<RefreshOutlinedIcon />}
            onClick={() => load(selectedUserId)}
            disabled={loading}
          >
            {t('hsi.refreshDnsCache')}
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
              title={data
                ? `${t('hsi.dnsCacheInfo')} — ${t('hsi.user')} ${selectedUserId} (${data.total_count} ${t('hsi.entries')})`
                : t('hsi.dnsCacheInfo')}
            >
              <DataTable
                columns={[
                  { label: t('hsi.dnsDomain') },
                  { label: 'QType' },
                  { label: 'TTL' },
                  { label: t('hsi.dnsRemaining') },
                  { label: t('hsi.dnsHitCount') }
                ]}
                loading={loading}
                isEmpty={entries.length === 0}
                emptyText={t('hsi.dnsCacheEmpty')}
              >
                {entries.slice(0, MAX_ROWS).map((entry, i) => (
                  <TableRow key={i} hover>
                    <TableCell><Mono>{entry.domain}</Mono></TableCell>
                    <TableCell><Mono dim>{entry.qtype}</Mono></TableCell>
                    <TableCell><Mono dim>{entry.ttl}</Mono></TableCell>
                    <TableCell><Mono dim>{entry.remaining_ttl}</Mono></TableCell>
                    <TableCell><Mono dim>{entry.hit_count}</Mono></TableCell>
                  </TableRow>
                ))}
              </DataTable>
              {entries.length > MAX_ROWS && (
                <Typography variant="caption" color="text.secondary">
                  {t('hsi.showing')} {MAX_ROWS} {t('hsi.of')} {data.total_count}
                </Typography>
              )}
            </SectionCard>
          )}
        </>
      )}
    </Stack>
  )
}
