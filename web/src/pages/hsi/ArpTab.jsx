import React, { useCallback, useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import Stack from '@mui/material/Stack'
import TableCell from '@mui/material/TableCell'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'
import RefreshOutlinedIcon from '@mui/icons-material/RefreshOutlined'
import { getArpTable } from '../../api'
import { useI18n } from '../../i18n/I18nContext'
import DataTable from './DataTable'
import Mono from './Mono'
import SectionCard from './SectionCard'
import UserSelect from './UserSelect'
import { extractApiError } from './utils'

const MAX_ROWS = 50

export default function ArpTab({ nodeId, userIds, selectedUserId, onSelectUser }) {
  const { t } = useI18n()
  const [data, setData] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const load = useCallback(async (userId) => {
    setLoading(true)
    setData(null)
    setError(null)
    try {
      setData(await getArpTable(nodeId, userId))
    } catch (err) {
      setError(extractApiError(err) || t('hsi.arpTableNotAvailable'))
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
        title={t('hsi.arpTable')}
        subtitle={t('hsi.arpTableHint')}
        action={selectedUserId ? (
          <Button
            variant="outlined"
            startIcon={<RefreshOutlinedIcon />}
            onClick={() => load(selectedUserId)}
            disabled={loading}
          >
            {t('hsi.refreshArpTable')}
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
                ? `${t('hsi.arpTableInfo')} — ${t('hsi.user')} ${selectedUserId} (${data.total_count} ${t('hsi.entries')})`
                : t('hsi.arpTableInfo')}
            >
              <DataTable
                columns={[{ label: 'Table ID' }, { label: 'IP' }, { label: 'MAC' }]}
                loading={loading}
                isEmpty={entries.length === 0}
                emptyText={t('hsi.arpTableEmpty')}
              >
                {entries.slice(0, MAX_ROWS).map((entry, i) => (
                  <TableRow key={i} hover>
                    <TableCell><Mono dim>{entry.entry_id}</Mono></TableCell>
                    <TableCell><Mono>{entry.ip}</Mono></TableCell>
                    <TableCell><Mono>{entry.mac}</Mono></TableCell>
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
