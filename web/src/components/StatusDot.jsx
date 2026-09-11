import React from 'react'
import Box from '@mui/material/Box'
import Stack from '@mui/material/Stack'
import Typography from '@mui/material/Typography'

// Status reads as a colored dot plus plain text — the console has no badges.
export default function StatusDot({ color, label, dim }) {
  return (
    <Stack direction="row" spacing={0.75} sx={{ alignItems: 'center' }}>
      <Box sx={{ width: 6, height: 6, borderRadius: '50%', bgcolor: color, flexShrink: 0 }} />
      <Typography variant="caption" sx={{ color: dim ? 'text.secondary' : 'text.primary', whiteSpace: 'nowrap' }}>
        {label}
      </Typography>
    </Stack>
  )
}
