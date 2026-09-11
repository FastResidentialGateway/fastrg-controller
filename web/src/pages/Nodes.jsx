import React, { useEffect, useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Skeleton from '@mui/material/Skeleton'
import Stack from '@mui/material/Stack'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableContainer from '@mui/material/TableContainer'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'
import { fetchNodes, apiClearInactiveNodes } from '../api'
import { PageActions } from '../components/AppShell'
import NodeRow from '../components/NodeRow'
import { useConfirm } from '../components/ConfirmProvider'
import { useNotify } from '../components/NotifyProvider'
import { useI18n } from '../i18n/I18nContext'

export default function Nodes(){
  const [nodes, setNodes] = useState([])
  const [error, setError] = useState(null)
  const [loading, setLoading] = useState(true)
  const [clearing, setClearing] = useState(false)
  const { t } = useI18n()
  const confirm = useConfirm()
  const { notify } = useNotify()

  const loadNodes = async () => {
    setLoading(true)
    setError(null)
    try{
      const data = await fetchNodes()
      setNodes(data)
    }catch(err){
      setError(err.message || t('nodes.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(()=>{
    loadNodes()
  }, [])

  const nodeList = Array.isArray(nodes) ? nodes : []
  const inactiveCount = nodeList.filter(n => n.status === 'inactive').length

  const handleClearInactive = async () => {
    if (inactiveCount === 0) return
    const accepted = await confirm({
      title: t('nodes.clearInactive').replace('{count}', inactiveCount),
      message: t('nodes.confirmClearInactive').replace('{count}', inactiveCount),
      confirmText: t('common.delete'),
      destructive: true,
    })
    if (!accepted) return

    setClearing(true)
    try {
      const data = await apiClearInactiveNodes()
      const deleted = (data && typeof data.deleted === 'number') ? data.deleted : inactiveCount
      notify(t('nodes.clearInactiveSuccess').replace('{count}', deleted), { severity: 'success' })
      await loadNodes()
    } catch (err) {
      const detail = err?.response?.data?.error || err.message || ''
      notify(`${t('nodes.clearInactiveFailed')}: ${detail}`, { severity: 'error', duration: 6000 })
    } finally {
      setClearing(false)
    }
  }

  // Column widths come from the cells themselves: the long ones truncate at a
  // fixed pixel width and keep the full value in a tooltip.
  const columns = [
    { label: t('nodes.status') },
    { label: t('nodes.uuid') },
    { label: t('nodes.location') },
    { label: t('nodes.nodeIp') },
    { label: t('nodes.version') },
    { label: t('nodes.nicModel') },
    { label: t('nodes.hostUptime') },
    { label: t('nodes.lastSeen') },
    { label: '', align: 'right' },
  ]

  return (
    <Stack spacing={2}>
      <PageActions>
        <Typography variant="caption" color="text.secondary">
          {t('nodes.count').replace('{count}', nodeList.length)}
        </Typography>
        {inactiveCount > 0 && (
          <Button variant="outlined" onClick={handleClearInactive} disabled={clearing}>
            {t('nodes.clearInactive').replace('{count}', inactiveCount)}
          </Button>
        )}
      </PageActions>

      {error && <Alert severity="error">{error}</Alert>}

      {loading ? (
        <Skeleton variant="rectangular" height={220} />
      ) : nodeList.length === 0 ? (
        <Box sx={{ py: 8, textAlign: 'center' }}>
          <Typography variant="body2" color="text.secondary">{t('nodes.noNodes')}</Typography>
        </Box>
      ) : (
        <Box sx={{ borderTop: 1, borderColor: 'divider' }}>
          <TableContainer>
            <Table sx={{ minWidth: 1240 }}>
              <TableHead>
                <TableRow>
                  {columns.map((col, i) => (
                    <TableCell key={i} align={col.align}>{col.label}</TableCell>
                  ))}
                </TableRow>
              </TableHead>
              <TableBody>
                {nodeList.map(n => (
                  <NodeRow
                    key={n.node_uuid || n.uuid || n.node_id || n.id || n.key}
                    node={n}
                    onNodeUnregistered={loadNodes}
                  />
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        </Box>
      )}
    </Stack>
  )
}
