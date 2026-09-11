import { useCallback, useEffect, useState } from 'react'
import { getHSIUserIds } from '../../api'
import { useNotify } from '../../components/NotifyProvider'
import { useI18n } from '../../i18n/I18nContext'
import { USER_ID_EXCEEDS, extractApiError } from './utils'

// Subscriber ids configured on a node, shared by every HSI tab.
export function useHsiUsers(nodeId) {
  const { t } = useI18n()
  const { notify } = useNotify()
  const [userIds, setUserIds] = useState([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const reload = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setUserIds(await getHSIUserIds(nodeId))
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.loadUserIdsFailed')
      if (msg === USER_ID_EXCEEDS) notify(t('hsi.error.userIdExceeds') || msg, { severity: 'error' })
      else setError(msg)
    } finally {
      setLoading(false)
    }
  }, [nodeId, notify, t])

  useEffect(() => {
    reload()
  }, [nodeId])

  return { userIds, setUserIds, loading, error, reload }
}
