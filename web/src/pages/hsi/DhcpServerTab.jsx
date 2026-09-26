import React, { useCallback, useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Skeleton from '@mui/material/Skeleton'
import Stack from '@mui/material/Stack'
import TableCell from '@mui/material/TableCell'
import TablePagination from '@mui/material/TablePagination'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'
import RefreshOutlinedIcon from '@mui/icons-material/RefreshOutlined'
import { getDhcpConfig } from '../../api'
import { useI18n } from '../../i18n/I18nContext'
import DataTable from './DataTable'
import Mono from './Mono'
import SectionCard from './SectionCard'
import UserSelect from './UserSelect'
import { extractApiError } from './utils'

const ROWS_PER_PAGE_OPTIONS = [10, 25, 50, 100]

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
  const [page, setPage] = useState(0)
  const [rowsPerPage, setRowsPerPage] = useState(25)

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
    setPage(0)
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
  const inuseIps = data?.inuse_ips || []
  // A refresh can shrink the list below the current page.
  const lastPage = Math.max(0, Math.ceil(inuseIps.length / rowsPerPage) - 1)
  const shownPage = Math.min(page, lastPage)

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
                </Box>
              ) : null}
            </SectionCard>
          )}
          {!error && (
            <SectionCard title={t('hsi.dhcpInuseIpsLabel')} sx={{ maxWidth: 720 }}>
              <DataTable
                columns={[{ label: 'IP' }]}
                loading={loading}
                isEmpty={inuseIps.length === 0}
                emptyText={t('hsi.dhcpLeaseNone')}
                maxHeight={480}
              >
                {inuseIps.slice(shownPage * rowsPerPage, (shownPage + 1) * rowsPerPage).map((ip) => (
                  <TableRow key={ip} hover>
                    <TableCell><Mono>{ip}</Mono></TableCell>
                  </TableRow>
                ))}
              </DataTable>
              {!loading && inuseIps.length > 0 && (
                <TablePagination
                  component="div"
                  count={inuseIps.length}
                  page={shownPage}
                  onPageChange={(_, next) => setPage(next)}
                  rowsPerPage={rowsPerPage}
                  rowsPerPageOptions={ROWS_PER_PAGE_OPTIONS}
                  onRowsPerPageChange={(e) => {
                    setRowsPerPage(parseInt(e.target.value, 10))
                    setPage(0)
                  }}
                  labelRowsPerPage={t('hsi.rowsPerPage')}
                  labelDisplayedRows={({ from, to, count }) => t('hsi.displayedRows')
                    .replace('{from}', from)
                    .replace('{to}', to)
                    .replace('{count}', count)}
                />
              )}
            </SectionCard>
          )}
        </>
      )}
    </Stack>
  )
}
