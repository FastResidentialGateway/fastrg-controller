import React from 'react'
import MenuItem from '@mui/material/MenuItem'
import TextField from '@mui/material/TextField'
import LabeledField from '../../components/LabeledField'
import { useI18n } from '../../i18n/I18nContext'

export default function UserSelect({ userIds, value, onChange, disabled }) {
  const { t } = useI18n()
  return (
    <LabeledField label={t('hsi.selectUserId')} width={180}>
      {({ id, labelId }) => (
        <TextField
          id={id}
          select
          value={value}
          onChange={(e) => onChange(e.target.value)}
          disabled={disabled}
          slotProps={{ select: { displayEmpty: true, labelId } }}
          fullWidth
        >
          <MenuItem value="">{t('hsi.selectUserId')}</MenuItem>
          {userIds.map(uid => (
            <MenuItem key={uid} value={uid} sx={{ fontFamily: '"JetBrains Mono", monospace' }}>{uid}</MenuItem>
          ))}
        </TextField>
      )}
    </LabeledField>
  )
}
