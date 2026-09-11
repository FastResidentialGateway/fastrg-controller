import React from 'react'
import Box from '@mui/material/Box'
import Skeleton from '@mui/material/Skeleton'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableContainer from '@mui/material/TableContainer'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import Typography from '@mui/material/Typography'

// Table with the three states every HSI listing needs: loading, empty, rows.
// `columns` are { label, align } entries; `children` are the body rows.
export default function DataTable({ columns, loading, isEmpty, emptyText, minWidth, maxHeight, children }) {
  if (loading) return <Skeleton variant="rectangular" height={120} />
  if (isEmpty) {
    return (
      <Box sx={{ py: 4 }}>
        <Typography variant="body2" color="text.secondary">{emptyText}</Typography>
      </Box>
    )
  }

  return (
    <TableContainer sx={{ maxHeight, borderTop: 1, borderColor: 'divider' }}>
      <Table stickyHeader sx={{ minWidth }}>
        <TableHead>
          <TableRow>
            {columns.map((col, i) => (
              <TableCell key={i} align={col.align}>{col.label}</TableCell>
            ))}
          </TableRow>
        </TableHead>
        <TableBody>{children}</TableBody>
      </Table>
    </TableContainer>
  )
}
