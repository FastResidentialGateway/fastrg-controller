import React from 'react'
import Box from '@mui/material/Box'
import Stack from '@mui/material/Stack'
import Typography from '@mui/material/Typography'

// A section is a hairline-separated block, not a raised card: overline title,
// optional description, and an action aligned to the right of the title row.
export default function SectionCard({ title, subtitle, action, children, sx }) {
  return (
    <Box sx={{ borderTop: 1, borderColor: 'divider', pt: 2, ...sx }}>
      {(title || action) && (
        <Stack
          direction="row"
          spacing={2}
          sx={{ alignItems: 'center', justifyContent: 'space-between', minHeight: 30, mb: subtitle ? 0.5 : 1.5 }}
        >
          {title && (
            <Typography variant="overline" color="text.secondary" component="h3">{title}</Typography>
          )}
          {action}
        </Stack>
      )}
      {subtitle && (
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 1.5 }}>
          {subtitle}
        </Typography>
      )}
      {children}
    </Box>
  )
}
