import { useCallback } from 'react'
import { getPPPoEStatus } from '../../api'
import { useConfirm } from '../../components/ConfirmProvider'
import { useI18n } from '../../i18n/I18nContext'

// The node redials PPPoE when ipv6_enable changes on a live session, so ask
// before cutting a connected subscriber off. A status that cannot be read asks
// too, with wording that does not claim a session exists. Resolves true when
// the subscriber is idle or the user agrees.
export function useIpv6RedialConfirm() {
  const confirm = useConfirm()
  const { t } = useI18n()

  return useCallback(async (nodeId, userId) => {
    let confirmKey = ''
    try {
      const status = await getPPPoEStatus(nodeId, userId)
      if (status?.phase === 'connected') confirmKey = 'hsi.confirmIpv6Redial'
    } catch (_) {
      confirmKey = 'hsi.confirmIpv6RedialUnknown'
    }
    if (!confirmKey) return true
    return confirm({
      title: t('hsi.ipv6Toggle'),
      message: t(confirmKey).replace('{userId}', userId)
    })
  }, [confirm, t])
}
