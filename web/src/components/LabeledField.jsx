import React, { useId } from 'react'
import Stack from '@mui/material/Stack'
import Typography from '@mui/material/Typography'

// Label sits above the control, and the control keeps its own width — dense
// forms instead of a column of full-width boxes with floating labels.
export default function LabeledField({ label, hint, width, children }) {
  const id = useId()
  const labelId = `${id}-label`
  return (
    <Stack spacing={0.5} sx={{ width }}>
      <Typography
        id={labelId}
        component="label"
        htmlFor={id}
        variant="caption"
        color="text.secondary"
        sx={{ lineHeight: 1.2 }}
      >
        {label}
      </Typography>
      {children({ id, labelId })}
      {hint && <Typography variant="caption" color="text.disabled">{hint}</Typography>}
    </Stack>
  )
}
