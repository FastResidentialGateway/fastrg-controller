import React from 'react'
import Box from '@mui/material/Box'

// Identifiers — user id, VLAN, IP, domain, TTL, ports, MAC, timestamps.
export default function Mono({ children, dim, nowrap = true }) {
  return (
    <Box component="code" sx={{ color: dim ? 'text.secondary' : 'text.primary', whiteSpace: nowrap ? 'nowrap' : 'normal' }}>
      {children}
    </Box>
  )
}
