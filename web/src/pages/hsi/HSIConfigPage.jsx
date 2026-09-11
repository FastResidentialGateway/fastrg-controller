import React, { useState } from 'react'
import { useParams, useSearchParams } from 'react-router-dom'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Tab from '@mui/material/Tab'
import Tabs from '@mui/material/Tabs'
import { useI18n } from '../../i18n/I18nContext'
import ArpTab from './ArpTab'
import DhcpServerTab from './DhcpServerTab'
import DnsCacheTab from './DnsCacheTab'
import DnsTab from './DnsTab'
import PppoeTab from './PppoeTab'
import SnatTab from './SnatTab'
import SwitchesTab from './SwitchesTab'
import { useHsiUsers } from './useHsiUsers'

// Tab keys double as the ?tab= value, so a reload lands on the same panel.
const TABS = [
  { key: 'pppoe', labelKey: 'hsi.pppoeConfig', Component: PppoeTab },
  { key: 'snat', labelKey: 'hsi.snatPortForwarding', Component: SnatTab },
  { key: 'dns', labelKey: 'dns.staticDnsRecord', Component: DnsTab },
  { key: 'switches', labelKey: 'hsi.otherSwitches', Component: SwitchesTab },
  { key: 'arp', labelKey: 'hsi.arpTable', Component: ArpTab },
  { key: 'dns-cache', labelKey: 'hsi.dnsCache', Component: DnsCacheTab },
  { key: 'dhcp-server', labelKey: 'hsi.dhcpServer', Component: DhcpServerTab }
]

export default function HSIConfigPage() {
  const { nodeId } = useParams()
  const { t } = useI18n()
  const [searchParams, setSearchParams] = useSearchParams()
  const [selectedUserId, setSelectedUserId] = useState('')
  const { userIds, setUserIds, error } = useHsiUsers(nodeId)

  const requested = searchParams.get('tab')
  const active = TABS.some(tab => tab.key === requested) ? requested : TABS[0].key
  const ActiveTab = TABS.find(tab => tab.key === active).Component

  const handleTabChange = (_event, key) => {
    setSearchParams({ tab: key }, { replace: true })
  }

  return (
    <Box>
      {/* The tab strip sits flush under the app header and spans the page. */}
      <Box sx={{ mx: -3, mt: -2.5, px: 3, borderBottom: 1, borderColor: 'divider' }}>
        <Tabs value={active} onChange={handleTabChange} variant="scrollable" scrollButtons="auto">
          {TABS.map(tab => <Tab key={tab.key} value={tab.key} label={t(tab.labelKey)} />)}
        </Tabs>
      </Box>

      <Box sx={{ pt: 2.5 }}>
        {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}
        <ActiveTab
          nodeId={nodeId}
          userIds={userIds}
          selectedUserId={selectedUserId}
          onSelectUser={setSelectedUserId}
          onUsersChanged={setUserIds}
        />
      </Box>
    </Box>
  )
}
