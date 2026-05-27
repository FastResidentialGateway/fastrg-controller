import React, { useState, useEffect } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import {
  getHSIUserIds,
  getHSIConfig,
  createHSIConfig,
  updateHSIConfig,
  deleteHSIConfig,
  dialPPPoE,
  hangupPPPoE,
  getDhcpLeaseCount,
  getArpTable,
  getDnsCache,
  getPPPoEInfo,
  getDhcpConfig,
  getDnsRecords,
  getDnsRecord,
  addOrUpdateDnsRecord,
  deleteDnsRecord,
  verifyAdminPassword
} from '../api'
import { useI18n } from '../i18n/I18nContext'
import useToast from '../components/ToastBridge'

export default function HSIConfig() {
  const { nodeId } = useParams()
  const navigate = useNavigate()
  const { t } = useI18n()

  const [currentStep, setCurrentStep] = useState(1) // 1: PPPoE Config, 2: DHCP Server Config
  const [action, setAction] = useState('')
  const [userIds, setUserIds] = useState([])
  const [selectedUserId, setSelectedUserId] = useState('')

  // PPPoE config
  const [pppoeConfig, setPppoeConfig] = useState({
    user_id: '',
    vlan_id: '',
    account_name: '',
    password: '',
    dns_proxy_enable: true,
    tcp_conntrack_enable: true,
    // enableStatus is returned from backend metadata as a string: "enabled", "enabling", "disabling", "disabled"
    enableStatus: ''
  })

  // DHCP server config
  const [dhcpConfig, setDhcpConfig] = useState({
    dhcp_addr_pool: '',
    dhcp_subnet: '',
    dhcp_gateway: ''
  })

  // Port mapping (SNAT) config
  const [portMappings, setPortMappings] = useState([])

  // DNS record state
  const [dnsRecords, setDnsRecords] = useState([])
  const [dnsForm, setDnsForm] = useState({ domain: '', ip: '', ttl: '' })
  const [dnsIsUpdate, setDnsIsUpdate] = useState(false)
  const [dnsAutoFillTimeout, setDnsAutoFillTimeout] = useState(null)
  const [dnsCheckingDomain, setDnsCheckingDomain] = useState(false)
  const [dnsLoading, setDnsLoading] = useState(false)

  // PPPoE panel state
  const [allPppoeConfigs, setAllPppoeConfigs] = useState([])
  const [pppoeIsUpdate, setPppoeIsUpdate] = useState(false)
  const [pppoePanelLoading, setPppoePanelLoading] = useState(false)
  // DHCP lease count state: { [userId]: { loading, data, error } }
  const [dhcpLeaseMap, setDhcpLeaseMap] = useState({})
  // ARP table state for single user
  const [arpTableLoading, setArpTableLoading] = useState(false)
  const [arpTableData, setArpTableData] = useState(null)
  const [arpTableError, setArpTableError] = useState(null)
  // DNS cache state for single user
  const [dnsCacheLoading, setDnsCacheLoading] = useState(false)
  const [dnsCacheData, setDnsCacheData] = useState(null)
  const [dnsCacheError, setDnsCacheError] = useState(null)
  // DHCP server config state for single user
  const [dhcpConfigLoading, setDhcpConfigLoading] = useState(false)
  const [dhcpConfigData, setDhcpConfigData] = useState(null)
  const [dhcpConfigError, setDhcpConfigError] = useState(null)
  // DNS proxy enable state for DNS tab
  const [dnsTabProxyEnable, setDnsTabProxyEnable] = useState(null)
  const [dnsTabProxyLoading, setDnsTabProxyLoading] = useState(false)
  // Other switches tab state
  const [tcpConntrackEnable, setTcpConntrackEnable] = useState(null)
  const [switchesLoading, setSwitchesLoading] = useState(false)
  // PPPoE info state for each user in PPPoE panel: { [userId]: { loading, data, error } }
  const [pppoeInfoMap, setPppoeInfoMap] = useState({})

  // Reveal password modal state
  const [revealModal, setRevealModal] = useState({
    open: false,
    userId: null,
    adminPassword: '',
    loading: false,
    error: ''
  })
  // Track which rows have had their password revealed in the table
  const [revealedPasswords, setRevealedPasswords] = useState({})

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  // Field validation states
  const [touchedFields, setTouchedFields] = useState({})
  const [fieldErrors, setFieldErrors] = useState({})
  const [autoFillTimeout, setAutoFillTimeout] = useState(null)
  const [isCheckingConfig, setIsCheckingConfig] = useState(false)
  const { showToast } = useToast()

  // Map backend enableStatus string to display label and color
  const getStatusInfo = (status) => {
    switch ((status || '').toLowerCase()) {
    case 'enabled':
      return { label: t('hsi.statusOn'), color: '#28a745' }
    case 'enabling':
      return { label: t('hsi.statusConnecting'), color: '#ffc107' }
    case 'disabling':
      return { label: t('hsi.statusDisconnecting'), color: '#ffc107' }
    case 'disabled':
    default:
      return { label: t('hsi.statusOff'), color: '#6c757d' }
    }
  }
  useEffect(() => {
    if (action === 'pppoe') {
      loadAllPppoeConfigs()
    } else if (action === 'snat' || action === 'dns' || action === 'arp' || action === 'dns-cache' || action === 'dhcp-server' || action === 'switches') {
      loadUserIds()
    }
  }, [action])

  const extractApiError = (err) => {
    // Prefer server-provided JSON error message when available
    try {
      return (err && err.response && err.response.data && err.response.data.error) || err.message || String(err)
    } catch (_) {
      return String(err)
    }
  }

  // Clear timeout
  useEffect(() => {
    return () => {
      if (autoFillTimeout) {
        clearTimeout(autoFillTimeout)
      }
      if (dnsAutoFillTimeout) {
        clearTimeout(dnsAutoFillTimeout)
      }
    }
  }, [])

  const loadUserIds = async () => {
    setLoading(true)
    setError(null)
    try {
      const ids = await getHSIUserIds(nodeId)
      setUserIds(ids)
    } catch (err) {
  const msg = extractApiError(err) || t('hsi.loadUserIdsFailed')
  if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
  else setError(msg)
    } finally {
      setLoading(false)
    }
  }

  const loadConfig = async (userId) => {
    setLoading(true)
    setError(null)
    try {
      const response = await getHSIConfig(nodeId, userId)
      // Handle nested structure: response has config and metadata
      const configData = response.config || response
      const metadata = response.metadata || {}

      setPppoeConfig({
        user_id: configData.user_id || '',
        vlan_id: configData.vlan_id || '',
        account_name: configData.account_name || '',
        password: configData.password || '',
        dns_proxy_enable: configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true,
        tcp_conntrack_enable: configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true,
        // store backend string state (enabled/enabling/disabling/disabled)
        enableStatus: metadata.enableStatus || ''
      })
      setDhcpConfig({
        dhcp_addr_pool: configData.dhcp_addr_pool || '',
        dhcp_subnet: configData.dhcp_subnet || '',
        dhcp_gateway: configData.dhcp_gateway || ''
      })
      setPortMappings(Array.isArray(configData['port-mapping']) ? configData['port-mapping'] : [])
    } catch (err) {
  const msg = extractApiError(err) || t('hsi.loadConfigFailed')
  if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
  else setError(msg)
    } finally {
      setLoading(false)
    }
  }

  // Load all PPPoE configs for the panel
  const loadAllPppoeConfigs = async () => {
    setPppoePanelLoading(true)
    setError(null)
    try {
      const ids = await getHSIUserIds(nodeId)
      setUserIds(ids)
      const configs = await Promise.all(
        ids.map(async (uid) => {
          try {
            const response = await getHSIConfig(nodeId, uid)
            const configData = response.config || response
            const metadata = response.metadata || {}
            return {
              user_id: configData.user_id || uid,
              vlan_id: configData.vlan_id || '',
              account_name: configData.account_name || '',
              password: configData.password || '',
              enableStatus: metadata.enableStatus || '',
              dns_proxy_enable: configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true,
              tcp_conntrack_enable: configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true,
              dhcp_addr_pool: configData.dhcp_addr_pool || '',
              dhcp_subnet: configData.dhcp_subnet || '',
              dhcp_gateway: configData.dhcp_gateway || ''
            }
          } catch (_) {
            return { user_id: uid, vlan_id: '', account_name: '', password: '', dns_proxy_enable: true, tcp_conntrack_enable: true, enableStatus: '' }
          }
        })
      )
      setAllPppoeConfigs(configs)
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.loadUserIdsFailed')
      if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
      else setError(msg)
    } finally {
      setPppoePanelLoading(false)
    }
  }

  // Silently auto-fill config for PPPoE panel (no confirm dialog)
  const silentAutoFillConfig = async (userId) => {
    if (!userId || userId === '') return
    setIsCheckingConfig(true)
    try {
      const response = await getHSIConfig(nodeId, userId)
      const configData = response.config || response
      const metadata = response.metadata || {}
      setPppoeConfig(prev => ({
        ...prev,
        vlan_id: configData.vlan_id || '',
        account_name: configData.account_name || '',
        password: configData.password || '',
        dns_proxy_enable: configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true,
        tcp_conntrack_enable: configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true,
        enableStatus: metadata.enableStatus || ''
      }))
      setDhcpConfig({
        dhcp_addr_pool: configData.dhcp_addr_pool || '',
        dhcp_subnet: configData.dhcp_subnet || '',
        dhcp_gateway: configData.dhcp_gateway || ''
      })
      setPortMappings(Array.isArray(configData['port-mapping']) ? configData['port-mapping'] : [])
      setPppoeIsUpdate(true)
    } catch (_) {
      setPppoeIsUpdate(false)
    } finally {
      setIsCheckingConfig(false)
    }
  }

  // Per-row handlers for PPPoE panel
  const handleDeletePppoeRow = async (userId) => {
    if (!window.confirm(t('hsi.confirmDelete').replace('{userId}', userId))) return
    setLoading(true)
    setError(null)
    try {
      await deleteHSIConfig(nodeId, userId)
      showToast(t('hsi.deleteSuccess'), 3500, 'info')
      await loadAllPppoeConfigs()
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.deleteFailed')
      if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
      else showToast(msg, 3500, 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleDialPppoeRow = async (userId) => {
    if (!window.confirm(t('hsi.confirmDial').replace('{userId}', userId))) return
    setLoading(true)
    setError(null)
    try {
      await dialPPPoE(nodeId, userId)
      showToast(t('hsi.dialSuccess'), 3500, 'info')
      await loadAllPppoeConfigs()
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.dialFailed')
      if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
      else showToast(msg, 3500, 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleHangupPppoeRow = async (userId) => {
    if (!window.confirm(t('hsi.confirmHangup').replace('{userId}', userId))) return
    setLoading(true)
    setError(null)
    try {
      await hangupPPPoE(nodeId, userId)
      showToast(t('hsi.hangupSuccess'), 3500, 'info')
      await loadAllPppoeConfigs()
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.hangupFailed')
      if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
      else showToast(msg, 3500, 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleOpenRevealModal = (cfg) => {
    setRevealModal({
      open: true,
      userId: cfg.user_id,
      adminPassword: '',
      loading: false,
      error: ''
    })
  }

  const handleCloseRevealModal = () => {
    setRevealModal({
      open: false,
      userId: null,
      adminPassword: '',
      loading: false,
      error: ''
    })
  }

  const handleVerifyAdminPassword = async () => {
    if (!revealModal.adminPassword) return
    setRevealModal(prev => ({ ...prev, loading: true, error: '' }))
    try {
      await verifyAdminPassword(revealModal.adminPassword)
      setRevealedPasswords(prev => ({ ...prev, [revealModal.userId]: true }))
      handleCloseRevealModal()
    } catch (err) {
      setRevealModal(prev => ({ ...prev, loading: false, error: t('hsi.revealPasswordModal.wrongPassword') }))
    }
  }

  const handleShowDhcpLease = async (userId) => {
    setDhcpLeaseMap(prev => ({ ...prev, [userId]: { loading: true, data: null, error: null } }))
    try {
      const data = await getDhcpLeaseCount(nodeId, userId)
      setDhcpLeaseMap(prev => ({ ...prev, [userId]: { loading: false, data, error: null } }))
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.dhcpLeaseNotAvailable')
      setDhcpLeaseMap(prev => ({ ...prev, [userId]: { loading: false, data: null, error: msg } }))
    }
  }

  const loadArpTable = async (userId) => {
    setArpTableLoading(true)
    setArpTableData(null)
    setArpTableError(null)
    try {
      const data = await getArpTable(nodeId, userId)
      setArpTableData(data)
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.arpTableNotAvailable')
      setArpTableError(msg)
    } finally {
      setArpTableLoading(false)
    }
  }

  const loadDnsCache = async (userId) => {
    setDnsCacheLoading(true)
    setDnsCacheData(null)
    setDnsCacheError(null)
    try {
      const data = await getDnsCache(nodeId, userId)
      setDnsCacheData(data)
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.dnsCacheNotAvailable')
      setDnsCacheError(msg)
    } finally {
      setDnsCacheLoading(false)
    }
  }

  const handleShowPPPoEInfo = async (userId) => {
    const current = pppoeInfoMap[userId]
    // If already expanded, collapse it
    if (current && current.expanded) {
      setPppoeInfoMap(prev => ({ ...prev, [userId]: { ...prev[userId], expanded: false } }))
      return
    }
    // If not expanded, expand and load if needed
    if (!current || !current.data) {
      setPppoeInfoMap(prev => ({ ...prev, [userId]: { loading: true, data: null, error: null, expanded: true } }))
      try {
        const data = await getPPPoEInfo(nodeId, userId)
        setPppoeInfoMap(prev => ({ ...prev, [userId]: { loading: false, data, error: null, expanded: true } }))
      } catch (err) {
        const msg = extractApiError(err) || t('hsi.pppoeInfoNotAvailable')
        setPppoeInfoMap(prev => ({ ...prev, [userId]: { loading: false, data: null, error: msg, expanded: true } }))
      }
    } else {
      // Data already loaded, just expand
      setPppoeInfoMap(prev => ({ ...prev, [userId]: { ...prev[userId], expanded: true } }))
    }
  }

  const loadDhcpConfig = async (userId) => {
    setDhcpConfigLoading(true)
    setDhcpConfigData(null)
    setDhcpConfigError(null)
    try {
      const data = await getDhcpConfig(nodeId, userId)
      setDhcpConfigData(data)
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.dhcpConfigNotAvailable')
      setDhcpConfigError(msg)
    } finally {
      setDhcpConfigLoading(false)
    }
  }

  const handleActionChange = (selectedAction) => {
    setAction(selectedAction)
    setError(null)
    setCurrentStep(1)
    setPppoeConfig({
      user_id: '',
      vlan_id: '',
      account_name: '',
      password: '',
      dns_proxy_enable: true,
      tcp_conntrack_enable: true
    })
    setDhcpConfig({
      dhcp_addr_pool: '',
      dhcp_subnet: '',
      dhcp_gateway: ''
    })
    setPortMappings([])
    setSelectedUserId('')
    // Reset DNS state
    setDnsRecords([])
    setDnsForm({ domain: '', ip: '', ttl: '' })
    setDnsIsUpdate(false)
    setDnsCheckingDomain(false)
    // Reset PPPoE panel state
    setAllPppoeConfigs([])
    setPppoeIsUpdate(false)
    setDhcpLeaseMap({})
    setRevealedPasswords({})
    // Reset ARP table state
    setArpTableLoading(false)
    setArpTableData(null)
    setArpTableError(null)
    // Reset DNS cache state
    setDnsCacheLoading(false)
    setDnsCacheData(null)
    setDnsCacheError(null)
    // Reset PPPoE info map
    setPppoeInfoMap({})
    // Reset DHCP config state
    setDhcpConfigLoading(false)
    setDhcpConfigData(null)
    setDhcpConfigError(null)
    // Reset DNS tab proxy state
    setDnsTabProxyEnable(null)
    setDnsTabProxyLoading(false)
    // Reset other switches tab state
    setTcpConntrackEnable(null)
    setSwitchesLoading(false)
    // Clear field validation states
    setTouchedFields({})
    setFieldErrors({})
  }

  const handleUserIdSelect = (userId) => {
    setSelectedUserId(userId)
    if (action === 'list' || action === 'snat') {
      loadConfig(userId)
    }
    if (action === 'dns') {
      // Load DNS records and dns_proxy_enable for this user
      loadDnsRecords(userId)
      loadDnsTabProxyEnable(userId)
    }
    if (action === 'switches') {
      loadSwitchesConfig(userId)
    }
    if (action === 'arp') {
      // Load ARP table for this user
      loadArpTable(userId)
    }
    if (action === 'dns-cache') {
      // Load DNS cache for this user
      loadDnsCache(userId)
    }
    if (action === 'dhcp-server') {
      // Load DHCP config for this user
      loadDhcpConfig(userId)
    }
  }

  const handleInputChange = async (field, value) => {
    if (currentStep === 1) {
      setPppoeConfig(prev => ({
        ...prev,
        [field]: value
      }))

      // Check and auto-fill existing config for user_id while creating/updating config
      if (field === 'user_id' && (action === 'create' || action === 'pppoe')) {
        // Create previous timeout
        if (autoFillTimeout) {
          clearTimeout(autoFillTimeout)
        }

        // Stop checking if input is cleared
        if (value.trim() === '') {
          setIsCheckingConfig(false)
          if (action === 'pppoe') setPppoeIsUpdate(false)
          return
        }

        // Set new timeout (500ms delay)
        const timeoutId = setTimeout(async () => {
          if (action === 'pppoe') {
            await silentAutoFillConfig(value.trim())
          } else {
            await checkAndAutoFillConfig(value.trim())
          }
        }, 500)

        setAutoFillTimeout(timeoutId)
      }
    } else {
      setDhcpConfig(prev => ({
        ...prev,
        [field]: value
      }))
    }

    // Clear field error state
    if (value.trim() !== '') {
      setFieldErrors(prev => ({
        ...prev,
        [field]: false
      }))
    }
  }

  // Check and auto-fill existing config for given userId
  const checkAndAutoFillConfig = async (userId) => {
    if (!userId || userId === '') return

    setIsCheckingConfig(true)

    try {
      // Try to fetch existing config
      const response = await getHSIConfig(nodeId, userId)
      // Handle nested structure
      const configData = response.config || response

      // If successfully retrieved settings, ask the user whether to auto-fill
      const autoTitle = t('hsi.autofillDetected').replace('{userId}', userId)
      const autoBodyLines = []
      autoBodyLines.push(t('hsi.autofillNoticePrefix'))
      autoBodyLines.push(`${t('hsi.vlanLabel')}: ${configData.vlan_id || t('common.notSet')}`)
      autoBodyLines.push(`${t('hsi.accountNameLabel')}: ${configData.account_name || t('common.notSet')}`)
      autoBodyLines.push(`${t('hsi.dhcpAddrPoolLabel')}: ${configData.dhcp_addr_pool || t('common.notSet')}`)
      autoBodyLines.push(`${t('hsi.subnetLabel')}: ${configData.dhcp_subnet || t('common.notSet')}`)
      autoBodyLines.push(`${t('hsi.gatewayLabel')}: ${configData.dhcp_gateway || t('common.notSet')}`)

      const shouldAutoFill = window.confirm(autoTitle + '\n\n' + autoBodyLines.join('\n'))

      if (shouldAutoFill) {
        // Auto-fill PPPoE settings (keep existing user_id)
        setPppoeConfig(prev => ({
          ...prev,
          vlan_id: configData.vlan_id || '',
          account_name: configData.account_name || '',
          password: configData.password || ''
        }))

        // Auto-fill DHCP settings
        setDhcpConfig({
          dhcp_addr_pool: configData.dhcp_addr_pool || '',
          dhcp_subnet: configData.dhcp_subnet || '',
          dhcp_gateway: configData.dhcp_gateway || ''
        })

        // Auto-fill port mappings
        setPortMappings(Array.isArray(configData['port-mapping']) ? configData['port-mapping'] : [])

        // Show success message (list filled fields)
        const filledFields = []
        if (configData.vlan_id) filledFields.push(t('hsi.vlanLabel'))
        if (configData.account_name) filledFields.push(t('hsi.accountNameLabel'))
        if (configData.dhcp_addr_pool) filledFields.push(t('hsi.dhcpAddrPoolLabel'))
        if (configData.dhcp_subnet) filledFields.push(t('hsi.subnetLabel'))
        if (configData.dhcp_gateway) filledFields.push(t('hsi.gatewayLabel'))

        if (filledFields.length > 0) {
          alert(t('hsi.autofillNoticePrefix') + ' ' + filledFields.join(', '))
        }
      }
    } catch (err) {
      // If the fetch fails, it means the user_id does not exist, which is normal.
      // Suppress debug logging in production.
    } finally {
      setIsCheckingConfig(false)
    }
  }

  // Process field focus event
  const handleFieldFocus = (field) => {
    setTouchedFields(prev => ({
      ...prev,
      [field]: true
    }))
  }

  // Process field blur event
  const handleFieldBlur = (field) => {
    const currentValue = currentStep === 1 ? 
      pppoeConfig[field] : 
      dhcpConfig[field]

    if (touchedFields[field] && (!currentValue || currentValue.trim() === '')) {
      setFieldErrors(prev => ({
        ...prev,
        [field]: true
      }))
    }
  }

  // Check if field has error
  const hasFieldError = (field) => {
    return fieldErrors[field] === true
  }

  const validatePPPoEConfig = () => {
    const { user_id, vlan_id, account_name, password } = pppoeConfig

    if (!user_id) return t('hsi.error.missingUserId')
    if (!vlan_id) return t('hsi.error.missingVlan')
    if (!account_name) return t('hsi.error.missingAccountName')
    if (!password) return t('hsi.error.missingPassword')

    // Validate user_id range (1-2000)
    const userIdNum = parseInt(user_id)
    if (isNaN(userIdNum) || userIdNum < 1 || userIdNum > 2000) {
      return t('hsi.error.userIdRange')
    }

    // Validate vlan_id range (2-4000)
    const vlanIdNum = parseInt(vlan_id)
    if (isNaN(vlanIdNum) || vlanIdNum < 2 || vlanIdNum > 4000) {
      return t('hsi.error.vlanRange')
    }

    return null
  }

  const validateDHCPConfig = () => {
    const { dhcp_addr_pool, dhcp_subnet, dhcp_gateway } = dhcpConfig

    if (!dhcp_addr_pool) return t('hsi.error.missingDhcpPool')
    if (!dhcp_subnet) return t('hsi.error.missingSubnet')
    if (!dhcp_gateway) return t('hsi.error.missingGateway')

    // Validate DHCP address pool format: include 'IP~IP' or 'IP-IP'
    const poolMatch = dhcp_addr_pool.match(/^(\d+\.\d+\.\d+\.\d+)[~-](\d+\.\d+\.\d+\.\d+)$/)
    if (!poolMatch) {
      return t('hsi.error.invalidDhcpPoolFormat')
    }

    const startIP = poolMatch[1]
    const endIP = poolMatch[2]

    // Check if private IP
    const isPrivateIP = (ip) => {
      const parts = ip.split('.').map(Number)
      return (parts[0] === 10) ||
             (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) ||
             (parts[0] === 192 && parts[1] === 168)
    }

    if (!isPrivateIP(startIP) || !isPrivateIP(endIP)) {
      return t('hsi.error.dhcpPoolPrivateIp')
    }

    // Check that IPs do not end with .0 or .255
    if (startIP.endsWith('.0') || startIP.endsWith('.255') || 
        endIP.endsWith('.0') || endIP.endsWith('.255')) {
      return t('hsi.error.dhcpPoolBadEnd')
    }

    // Validate subnet mask
    const subnetParts = dhcp_subnet.split('.').map(Number)
    if (subnetParts.length !== 4 || subnetParts.some(part => isNaN(part) || part < 0 || part > 255)) {
      return t('hsi.error.invalidSubnetMask')
    }
    
    // Check subnet mask validity (simple check)
    const gatewayParts = dhcp_gateway.split('.').map(Number)
    const startParts = startIP.split('.').map(Number)

    if (startParts[0] === 192 && startParts[1] === 168) {
      if (!dhcp_subnet.startsWith('255.255')) {
        return t('hsi.error.subnetMask192')
      }
    } else if (startParts[0] === 10) {
      if (!dhcp_subnet.startsWith('255.')) {
        return t('hsi.error.subnetMask10')
      }
    }

    // Validate gateway IP
    if (gatewayParts.length !== 4 || gatewayParts.some(part => isNaN(part) || part < 0 || part > 255)) {
      return t('hsi.error.invalidGateway')
    }

    if (dhcp_gateway.endsWith('.0')) {
      return t('hsi.error.gatewayEndsZero')
    }

    // Check if gateway IP is in the same subnet as the DHCP address pool
    const sameSubnet = startParts.every((part, index) => {
      const mask = subnetParts[index]
      return (part & mask) === (gatewayParts[index] & mask)
    })

    if (!sameSubnet) {
      return t('hsi.error.gatewayNotSameSubnet')
    }

    // Check whether gateway IP is within the DHCP address pool range
    const ipToNum = (ip) => {
      return ip.split('.').reduce((num, octet) => (num << 8) + parseInt(octet), 0) >>> 0
    }

    const startNum = ipToNum(startIP)
    const endNum = ipToNum(endIP)
    const gatewayNum = ipToNum(dhcp_gateway)

    if (gatewayNum >= startNum && gatewayNum <= endNum) {
      return t('hsi.error.gatewayInPool')
    }

    return null
  }

  // Port mapping validation
  const validatePortMappings = () => {
    for (let i = 0; i < portMappings.length; i++) {
      const pm = portMappings[i]
      if (!pm.dip || pm.dip.trim() === '') {
        return t('hsi.error.portMapping.missingDip').replace('{index}', String(i + 1))
      }
      // Validate IP format
      const ipParts = pm.dip.split('.').map(Number)
      if (ipParts.length !== 4 || ipParts.some(p => isNaN(p) || p < 0 || p > 255)) {
        return t('hsi.error.portMapping.invalidDip').replace('{index}', String(i + 1))
      }
      if (!pm.dport || pm.dport.trim() === '') {
        return t('hsi.error.portMapping.missingDport').replace('{index}', String(i + 1))
      }
      const dportNum = parseInt(pm.dport)
      if (isNaN(dportNum) || dportNum < 1 || dportNum > 65535) {
        return t('hsi.error.portMapping.invalidPort').replace('{index}', String(i + 1))
      }
      if (!pm.eport || pm.eport.trim() === '') {
        return t('hsi.error.portMapping.missingEport').replace('{index}', String(i + 1))
      }
      const eportNum = parseInt(pm.eport)
      if (isNaN(eportNum) || eportNum < 1 || eportNum > 65535) {
        return t('hsi.error.portMapping.invalidPort').replace('{index}', String(i + 1))
      }
    }
    return null
  }

  // Port mapping helpers
  const addPortMapping = () => {
    setPortMappings(prev => [...prev, { dip: '', dport: '', eport: '' }])
  }

  const removePortMapping = (index) => {
    setPortMappings(prev => prev.filter((_, i) => i !== index))
  }

  const updatePortMapping = (index, field, value) => {
    setPortMappings(prev => prev.map((pm, i) => i === index ? { ...pm, [field]: value } : pm))
  }

  const handleNextStep = () => {
    const validationError = validatePPPoEConfig()
    if (validationError) {
      alert(validationError)
      return
    }
    setCurrentStep(2)
    // Clear field validation states when going to second step
    setTouchedFields({})
    setFieldErrors({})
  }

  const handleCreateOrUpdate = async () => {
    if (currentStep === 1) {
      // Step 1: Validate PPPoE config and go to next step
      handleNextStep()
      return
    }

    // Step 2: Validate DHCP config and submit
    const dhcpValidationError = validateDHCPConfig()
    if (dhcpValidationError) {
      alert(dhcpValidationError)
      return
    }

    setLoading(true)
    setError(null)
    try {
      // Check if config already exists
      let exists = false
      try {
        await getHSIConfig(nodeId, pppoeConfig.user_id)
        exists = true
      } catch (err) {
        // Set to false if not found
        exists = false
      }

      // Build payload only with HSIConfig fields (do not send UI-only fields like enableStatus)
      const fullConfig = {
        user_id: pppoeConfig.user_id,
        vlan_id: pppoeConfig.vlan_id,
        account_name: pppoeConfig.account_name,
        password: pppoeConfig.password,
        dns_proxy_enable: pppoeConfig.dns_proxy_enable,
        tcp_conntrack_enable: pppoeConfig.tcp_conntrack_enable,
        dhcp_addr_pool: dhcpConfig.dhcp_addr_pool,
        dhcp_subnet: dhcpConfig.dhcp_subnet,
        dhcp_gateway: dhcpConfig.dhcp_gateway
      }

      if (exists) {
        await updateHSIConfig(nodeId, pppoeConfig.user_id, fullConfig)
        alert(t('hsi.saveSuccess'))
      } else {
        await createHSIConfig(nodeId, fullConfig)
        alert(t('hsi.saveSuccess'))
      }

      // Reset list
      setCurrentStep(1)
      setPppoeConfig({
        user_id: '',
        vlan_id: '',
        account_name: '',
        password: '',
        dns_proxy_enable: true,
        tcp_conntrack_enable: true
      })
      setDhcpConfig({
        dhcp_addr_pool: '',
        dhcp_subnet: '',
        dhcp_gateway: ''
      })
      setPortMappings([])
      // Reload configs for PPPoE panel
      if (action === 'pppoe') {
        setPppoeIsUpdate(false)
        await loadAllPppoeConfigs()
      }
    } catch (err) {
  const msg = extractApiError(err) || t('hsi.saveFailed')
  if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
  else setError(msg)
    } finally {
      setLoading(false)
    }
  }

  const handleDelete = async () => {
    if (!selectedUserId) {
      alert(t('hsi.selectUserIdToDelete'))
      return
    }
    if (!window.confirm(t('hsi.confirmDelete').replace('{userId}', selectedUserId))) {
      return
    }

    setLoading(true)
    setError(null)
    try {
      await deleteHSIConfig(nodeId, selectedUserId)
  alert(t('hsi.deleteSuccess'))
      setSelectedUserId('')
      loadUserIds() // Reload list
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.deleteFailed')
      if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
      else setError(msg)
    } finally {
      setLoading(false)
    }
  }

  const handleDial = async () => {
    if (!selectedUserId) {
      alert(t('hsi.selectUserIdToDial'))
      return
    }
    if (!window.confirm(t('hsi.confirmDial').replace('{userId}', selectedUserId))) {
      return
    }

    setLoading(true)
    setError(null)
    try {
      await dialPPPoE(nodeId, selectedUserId)
      alert(t('hsi.dialSuccess'))
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.dialFailed')
      if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
      else setError(msg)
    } finally {
      setLoading(false)
    }
  }

  const handleHangup = async () => {
    if (!selectedUserId) {
      alert(t('hsi.selectUserIdToHangup'))
      return
    }
    if (!window.confirm(t('hsi.confirmHangup').replace('{userId}', selectedUserId))) {
      return
    }

    setLoading(true)
    setError(null)
    try {
      await hangupPPPoE(nodeId, selectedUserId)
      alert(t('hsi.hangupSuccess'))
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.hangupFailed')
      if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
      else setError(msg)
    } finally {
      setLoading(false)
    }
  }

  // ===== DNS Record Functions =====
  const loadDnsRecords = async (userId) => {
    setDnsLoading(true)
    setDnsRecords([])
    setDnsForm({ domain: '', ip: '', ttl: '' })
    setDnsIsUpdate(false)
    try {
      const records = await getDnsRecords(nodeId, userId)
      setDnsRecords(records)
    } catch (err) {
      // 404 is normal (no records yet)
      if (!err.response || err.response.status !== 404) {
        const msg = extractApiError(err) || t('dns.loadFailed')
        showToast(msg, 3500, 'error')
      }
      setDnsRecords([])
    } finally {
      setDnsLoading(false)
    }
  }

  const handleDnsDomainChange = (value) => {
    setDnsForm(prev => ({ ...prev, domain: value }))
    setDnsIsUpdate(false)

    // Clear previous timeout
    if (dnsAutoFillTimeout) {
      clearTimeout(dnsAutoFillTimeout)
    }

    if (value.trim() === '' || !selectedUserId) {
      setDnsCheckingDomain(false)
      return
    }

    // Debounced domain lookup
    const timeoutId = setTimeout(async () => {
      setDnsCheckingDomain(true)
      try {
        const record = await getDnsRecord(nodeId, selectedUserId, value.trim())
        // Record exists — auto-fill IP + TTL for update
        setDnsForm(prev => ({
          ...prev,
          ip: record.ip || '',
          ttl: record.ttl !== undefined ? String(record.ttl) : ''
        }))
        setDnsIsUpdate(true)
      } catch (_) {
        // Not found — new record mode
        setDnsIsUpdate(false)
      } finally {
        setDnsCheckingDomain(false)
      }
    }, 500)
    setDnsAutoFillTimeout(timeoutId)
  }

  const handleAddOrUpdateDns = async () => {
    const { domain, ip, ttl } = dnsForm
    if (!domain.trim()) {
      alert(t('dns.error.missingDomain'))
      return
    }
    if (!ip.trim()) {
      alert(t('dns.error.missingIp'))
      return
    }
    const ttlNum = parseInt(ttl, 10)
    if (isNaN(ttlNum) || ttlNum <= 0) {
      alert(t('dns.error.invalidTtl'))
      return
    }

    // Validate IP format
    const ipParts = ip.split('.').map(Number)
    if (ipParts.length !== 4 || ipParts.some(p => isNaN(p) || p < 0 || p > 255)) {
      alert(t('dns.error.invalidIp'))
      return
    }

    setLoading(true)
    try {
      const result = await addOrUpdateDnsRecord(nodeId, selectedUserId, {
        domain: domain.trim(),
        ip: ip.trim(),
        ttl: ttlNum
      })
      showToast(
        result.action === 'updated' ? t('dns.updateSuccess') : t('dns.addSuccess'),
        3500,
        'info'
      )
      setDnsForm({ domain: '', ip: '', ttl: '' })
      setDnsIsUpdate(false)
      await loadDnsRecords(selectedUserId)
    } catch (err) {
      const isMaxRecordsError = err.response && err.response.status === 422
      const msg = isMaxRecordsError ? t('dns.error.maxRecords') : (extractApiError(err) || t('dns.saveFailed'))
      showToast(msg, 4500, 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleDeleteDns = async (domain) => {
    if (!window.confirm(t('dns.confirmDelete').replace('{domain}', domain))) {
      return
    }
    setLoading(true)
    try {
      await deleteDnsRecord(nodeId, selectedUserId, domain)
      showToast(t('dns.deleteSuccess'), 3500, 'info')
      await loadDnsRecords(selectedUserId)
    } catch (err) {
      const msg = extractApiError(err) || t('dns.deleteFailed')
      showToast(msg, 4500, 'error')
    } finally {
      setLoading(false)
    }
  }

  const handleSaveSnat = async () => {
    if (!selectedUserId) {
      alert(t('hsi.selectUserId'))
      return
    }

    const portMappingError = validatePortMappings()
    if (portMappingError) {
      alert(portMappingError)
      return
    }

    setLoading(true)
    setError(null)
    try {
      // Load existing config to preserve all fields
      const response = await getHSIConfig(nodeId, selectedUserId)
      const configData = response.config || response

      // Build payload with existing fields + updated port-mapping
      const fullConfig = {
        user_id: configData.user_id || selectedUserId,
        vlan_id: configData.vlan_id || '',
        account_name: configData.account_name || '',
        password: configData.password || '',
        dns_proxy_enable: configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true,
        tcp_conntrack_enable: configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true,
        dhcp_addr_pool: configData.dhcp_addr_pool || '',
        dhcp_subnet: configData.dhcp_subnet || '',
        dhcp_gateway: configData.dhcp_gateway || ''
      }

      if (portMappings.length > 0) {
        fullConfig['port-mapping'] = portMappings.map((pm, idx) => ({
          index: String(idx),
          dip: pm.dip,
          dport: pm.dport,
          eport: pm.eport
        }))
      }

      await updateHSIConfig(nodeId, selectedUserId, fullConfig)
      alert(t('hsi.saveSuccess'))
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.saveFailed')
      if (msg === 'User ID exceeds subscriber count') showToast(t('hsi.error.userIdExceeds') || msg, 3500, 'error')
      else setError(msg)
    } finally {
      setLoading(false)
    }
  }

  const loadDnsTabProxyEnable = async (userId) => {
    setDnsTabProxyLoading(true)
    setDnsTabProxyEnable(null)
    try {
      const response = await getHSIConfig(nodeId, userId)
      const configData = response.config || response
      setDnsTabProxyEnable(configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true)
    } catch (_) {
      setDnsTabProxyEnable(true)
    } finally {
      setDnsTabProxyLoading(false)
    }
  }

  const handleDnsTabToggleProxy = async () => {
    if (!selectedUserId || dnsTabProxyEnable === null) return
    setDnsTabProxyLoading(true)
    try {
      const response = await getHSIConfig(nodeId, selectedUserId)
      const configData = response.config || response
      const newValue = !dnsTabProxyEnable
      const fullConfig = {
        user_id: configData.user_id || selectedUserId,
        vlan_id: configData.vlan_id || '',
        account_name: configData.account_name || '',
        password: configData.password || '',
        dns_proxy_enable: newValue,
        tcp_conntrack_enable: configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true,
        dhcp_addr_pool: configData.dhcp_addr_pool || '',
        dhcp_subnet: configData.dhcp_subnet || '',
        dhcp_gateway: configData.dhcp_gateway || ''
      }
      if (Array.isArray(configData['port-mapping']) && configData['port-mapping'].length > 0) {
        fullConfig['port-mapping'] = configData['port-mapping']
      }
      await updateHSIConfig(nodeId, selectedUserId, fullConfig)
      setDnsTabProxyEnable(newValue)
      showToast(t('hsi.saveSuccess'), 3500, 'info')
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.saveFailed')
      showToast(msg, 3500, 'error')
    } finally {
      setDnsTabProxyLoading(false)
    }
  }

  const loadSwitchesConfig = async (userId) => {
    setSwitchesLoading(true)
    setTcpConntrackEnable(null)
    try {
      const response = await getHSIConfig(nodeId, userId)
      const configData = response.config || response
      setTcpConntrackEnable(configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true)
    } catch (_) {
      setTcpConntrackEnable(true)
    } finally {
      setSwitchesLoading(false)
    }
  }

  const handleToggleTcpConntrack = async () => {
    if (!selectedUserId || tcpConntrackEnable === null) return
    setSwitchesLoading(true)
    try {
      const response = await getHSIConfig(nodeId, selectedUserId)
      const configData = response.config || response
      const newValue = !tcpConntrackEnable
      const fullConfig = {
        user_id: configData.user_id || selectedUserId,
        vlan_id: configData.vlan_id || '',
        account_name: configData.account_name || '',
        password: configData.password || '',
        dns_proxy_enable: configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true,
        tcp_conntrack_enable: newValue,
        dhcp_addr_pool: configData.dhcp_addr_pool || '',
        dhcp_subnet: configData.dhcp_subnet || '',
        dhcp_gateway: configData.dhcp_gateway || ''
      }
      if (Array.isArray(configData['port-mapping']) && configData['port-mapping'].length > 0) {
        fullConfig['port-mapping'] = configData['port-mapping']
      }
      await updateHSIConfig(nodeId, selectedUserId, fullConfig)
      setTcpConntrackEnable(newValue)
      showToast(t('hsi.saveSuccess'), 3500, 'info')
    } catch (err) {
      const msg = extractApiError(err) || t('hsi.saveFailed')
      showToast(msg, 3500, 'error')
    } finally {
      setSwitchesLoading(false)
    }
  }

  return (
    <div style={{ padding: '20px' }}>
      <div style={{ marginBottom: '20px', display: 'flex', alignItems: 'center', gap: '10px' }}>
        <button 
          onClick={() => navigate('/nodes')}
          style={{
            backgroundColor: '#6c757d',
            color: 'white',
            border: 'none',
            borderRadius: '4px',
            padding: '8px 12px',
            cursor: 'pointer'
          }}
        >
          ← {t('hsi.back')}
        </button>
        <h2>{t('hsi.title')} - {t('hsi.node')}: {nodeId}</h2>
      </div>

      {error && (
        <div style={{ 
          backgroundColor: '#f8d7da', 
          color: '#721c24', 
          padding: '10px', 
          borderRadius: '4px', 
          marginBottom: '20px' 
        }}>
          {error}
        </div>
      )}


      {/* Choose an action */}
      <div style={{ marginBottom: '30px' }}>
        <h3>{t('hsi.chooseAction')}</h3>
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '10px' }}>
          {[
            { key: 'pppoe', labelKey: 'hsi.pppoeConfig' },
            { key: 'dhcp-server', labelKey: 'hsi.dhcpServer' },
            { key: 'snat', labelKey: 'hsi.snatPortForwarding' },
            { key: 'dns', labelKey: 'dns.staticDnsRecord' },
            { key: 'arp', labelKey: 'hsi.arpTable' },
            { key: 'dns-cache', labelKey: 'hsi.dnsCache' },
            { key: 'switches', labelKey: 'hsi.otherSwitches' }
          ].map(({ key, labelKey }) => (
            <button
              key={key}
              onClick={() => handleActionChange(key)}
              style={{
                backgroundColor: action === key ? '#007bff' : '#e9ecef',
                color: action === key ? 'white' : '#495057',
                border: '1px solid #dee2e6',
                borderRadius: '4px',
                padding: '10px 15px',
                cursor: 'pointer'
              }}
            >
              {t(labelKey)}
            </button>
          ))}
        </div>
      </div>

      {/* ===== PPPoE Configuration Panel ===== */}
      {action === 'pppoe' && (
        <div>
          <h3>{t('hsi.pppoeConfig')}</h3>
          <p style={{ color: '#6c757d', marginBottom: '15px', fontSize: '14px' }}>
            {t('hsi.pppoeHint')}
          </p>

          {/* Add / Update PPPoE config form */}
          <div style={{
            padding: '15px',
            backgroundColor: '#f8f9fa',
            borderRadius: '8px',
            border: '1px solid #dee2e6',
            marginBottom: '20px',
            maxWidth: '500px'
          }}>
            {currentStep === 1 ? (
              <div>
                <h4 style={{ marginTop: 0 }}>
                  {pppoeIsUpdate ? t('hsi.updatePppoeConfig') : t('hsi.createPppoe')}
                  {isCheckingConfig && (
                    <span style={{ marginLeft: '10px', color: '#007bff', fontSize: '12px' }}>
                      {t('hsi.checkingConfig')}
                    </span>
                  )}
                </h4>
                {pppoeIsUpdate && (
                  <div style={{
                    padding: '8px 12px',
                    backgroundColor: '#fff3cd',
                    borderRadius: '4px',
                    border: '1px solid #ffc107',
                    marginBottom: '12px',
                    fontSize: '13px',
                    color: '#856404'
                  }}>
                    {t('hsi.pppoeUpdateNotice')}
                  </div>
                )}
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>
                    {t('hsi.userId')} (1-2000):
                  </label>
                  {hasFieldError('user_id') && (
                    <div style={{ color: '#dc3545', fontSize: '12px', marginBottom: '3px' }}>
                      {t('common.fieldRequired')}
                    </div>
                  )}
                  <input
                    type="text"
                    value={pppoeConfig.user_id}
                    onChange={(e) => handleInputChange('user_id', e.target.value)}
                    onFocus={() => handleFieldFocus('user_id')}
                    onBlur={() => handleFieldBlur('user_id')}
                    style={{
                      width: '100%',
                      padding: '8px',
                      border: hasFieldError('user_id') ? '2px solid #dc3545' : '1px solid #ccc',
                      borderRadius: '4px'
                    }}
                  />
                </div>
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.vlanLabel')} (2-4000):</label>
                  {hasFieldError('vlan_id') && (
                    <div style={{ color: '#dc3545', fontSize: '12px', marginBottom: '3px' }}>
                      {t('common.fieldRequired')}
                    </div>
                  )}
                  <input
                    type="text"
                    value={pppoeConfig.vlan_id}
                    onChange={(e) => handleInputChange('vlan_id', e.target.value)}
                    onFocus={() => handleFieldFocus('vlan_id')}
                    onBlur={() => handleFieldBlur('vlan_id')}
                    style={{
                      width: '100%',
                      padding: '8px',
                      border: hasFieldError('vlan_id') ? '2px solid #dc3545' : '1px solid #ccc',
                      borderRadius: '4px'
                    }}
                  />
                </div>
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.accountNameLabel')}:</label>
                  {hasFieldError('account_name') && (
                    <div style={{ color: '#dc3545', fontSize: '12px', marginBottom: '3px' }}>
                      {t('common.fieldRequired')}
                    </div>
                  )}
                  <input
                    type="text"
                    value={pppoeConfig.account_name}
                    onChange={(e) => handleInputChange('account_name', e.target.value)}
                    onFocus={() => handleFieldFocus('account_name')}
                    onBlur={() => handleFieldBlur('account_name')}
                    style={{
                      width: '100%',
                      padding: '8px',
                      border: hasFieldError('account_name') ? '2px solid #dc3545' : '1px solid #ccc',
                      borderRadius: '4px'
                    }}
                  />
                </div>
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.password')}:</label>
                  {hasFieldError('password') && (
                    <div style={{ color: '#dc3545', fontSize: '12px', marginBottom: '3px' }}>
                      {t('common.fieldRequired')}
                    </div>
                  )}
                  <input
                    type="password"
                    value={pppoeConfig.password}
                    onChange={(e) => handleInputChange('password', e.target.value)}
                    onFocus={() => handleFieldFocus('password')}
                    onBlur={() => handleFieldBlur('password')}
                    style={{
                      width: '100%',
                      padding: '8px',
                      border: hasFieldError('password') ? '2px solid #dc3545' : '1px solid #ccc',
                      borderRadius: '4px'
                    }}
                  />
                </div>
                <button
                  onClick={handleCreateOrUpdate}
                  disabled={loading}
                  style={{
                    backgroundColor: pppoeIsUpdate ? '#ffc107' : '#007bff',
                    color: pppoeIsUpdate ? '#000' : 'white',
                    border: 'none',
                    borderRadius: '4px',
                    padding: '10px 20px',
                    cursor: loading ? 'not-allowed' : 'pointer'
                  }}
                >
                  {loading ? t('common.processing') : t('hsi.nextStepDhcp')}
                </button>
              </div>
            ) : (
              <div>
                <h4 style={{ marginTop: 0 }}>{t('hsi.step2Dhcp')}</h4>
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.dhcpAddrPoolLabel')}:</label>
                  {hasFieldError('dhcp_addr_pool') && (
                    <div style={{ color: '#dc3545', fontSize: '12px', marginBottom: '3px' }}>
                      {t('common.fieldRequired')}
                    </div>
                  )}
                  <input
                    type="text"
                    placeholder={t('hsi.example.dhcpPool')}
                    value={dhcpConfig.dhcp_addr_pool}
                    onChange={(e) => handleInputChange('dhcp_addr_pool', e.target.value)}
                    onFocus={() => handleFieldFocus('dhcp_addr_pool')}
                    onBlur={() => handleFieldBlur('dhcp_addr_pool')}
                    style={{
                      width: '100%',
                      padding: '8px',
                      border: hasFieldError('dhcp_addr_pool') ? '2px solid #dc3545' : '1px solid #ccc',
                      borderRadius: '4px'
                    }}
                  />
                </div>
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.subnetLabel')}:</label>
                  {hasFieldError('dhcp_subnet') && (
                    <div style={{ color: '#dc3545', fontSize: '12px', marginBottom: '3px' }}>
                      {t('common.fieldRequired')}
                    </div>
                  )}
                  <input
                    type="text"
                    placeholder={t('hsi.example.subnet')}
                    value={dhcpConfig.dhcp_subnet}
                    onChange={(e) => handleInputChange('dhcp_subnet', e.target.value)}
                    onFocus={() => handleFieldFocus('dhcp_subnet')}
                    onBlur={() => handleFieldBlur('dhcp_subnet')}
                    style={{
                      width: '100%',
                      padding: '8px',
                      border: hasFieldError('dhcp_subnet') ? '2px solid #dc3545' : '1px solid #ccc',
                      borderRadius: '4px'
                    }}
                  />
                </div>
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.gatewayLabel')}:</label>
                  {hasFieldError('dhcp_gateway') && (
                    <div style={{ color: '#dc3545', fontSize: '12px', marginBottom: '3px' }}>
                      {t('common.fieldRequired')}
                    </div>
                  )}
                  <input
                    type="text"
                    placeholder={t('hsi.example.gateway')}
                    value={dhcpConfig.dhcp_gateway}
                    onChange={(e) => handleInputChange('dhcp_gateway', e.target.value)}
                    onFocus={() => handleFieldFocus('dhcp_gateway')}
                    onBlur={() => handleFieldBlur('dhcp_gateway')}
                    style={{
                      width: '100%',
                      padding: '8px',
                      border: hasFieldError('dhcp_gateway') ? '2px solid #dc3545' : '1px solid #ccc',
                      borderRadius: '4px'
                    }}
                  />
                </div>
                <div style={{ display: 'flex', gap: '10px' }}>
                  <button
                    onClick={() => {
                      setCurrentStep(1)
                      setTouchedFields({})
                      setFieldErrors({})
                    }}
                    style={{
                      backgroundColor: '#6c757d',
                      color: 'white',
                      border: 'none',
                      borderRadius: '4px',
                      padding: '10px 20px',
                      cursor: 'pointer'
                    }}
                  >
                    {t('common.back')}
                  </button>
                  <button
                    onClick={handleCreateOrUpdate}
                    disabled={loading}
                    style={{
                      backgroundColor: pppoeIsUpdate ? '#ffc107' : '#007bff',
                      color: pppoeIsUpdate ? '#000' : 'white',
                      border: 'none',
                      borderRadius: '4px',
                      padding: '10px 20px',
                      cursor: loading ? 'not-allowed' : 'pointer'
                    }}
                  >
                    {loading ? t('common.processing') : t('hsi.confirm')}
                  </button>
                </div>
              </div>
            )}
          </div>

          {/* Existing PPPoE configurations table */}
          <h4>{t('hsi.currentPppoeConfigs')}</h4>
          {pppoePanelLoading ? (
            <div style={{ textAlign: 'center', padding: '15px' }}>{t('common.loading')}</div>
          ) : allPppoeConfigs.length === 0 ? (
            <div style={{ color: '#6c757d', marginBottom: '10px' }}>{t('hsi.noPppoeConfigs')}</div>
          ) : (
            <div style={{ overflowX: 'auto' }}>
              <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: '10px', minWidth: '900px' }}>
                <thead>
                  <tr style={{ backgroundColor: '#f8f9fa' }}>
                    <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.userId')}</th>
                    <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.vlanLabel')}</th>
                    <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.accountNameLabel')}</th>
                    <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.password')}</th>
                    <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.status')}</th>
                    <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'center' }}>{t('hsi.actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {allPppoeConfigs.map((cfg, idx) => {
                    const statusInfo = getStatusInfo(cfg.enableStatus)
                    return (
                      <React.Fragment key={idx}>
                        <tr>
                          <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{cfg.user_id}</td>
                          <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{cfg.vlan_id}</td>
                          <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{cfg.account_name}</td>
                          <td style={{ border: '1px solid #dee2e6', padding: '8px', whiteSpace: 'nowrap' }}>
                            {revealedPasswords[cfg.user_id] ? (
                              <>
                                <span style={{ fontFamily: 'monospace', marginRight: '6px' }}>{cfg.password}</span>
                                <button
                                  onClick={() => setRevealedPasswords(prev => { const n = { ...prev }; delete n[cfg.user_id]; return n })}
                                  title={t('hsi.hidePassword')}
                                  style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '0 2px', color: '#6c757d', verticalAlign: 'middle' }}
                                >
                                  <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                                    <path d="M17.94 17.94A10.07 10.07 0 0112 20c-7 0-11-8-11-8a18.45 18.45 0 015.06-5.94"/>
                                    <path d="M9.9 4.24A9.12 9.12 0 0112 4c7 0 11 8 11 8a18.5 18.5 0 01-2.16 3.19"/>
                                    <path d="M14.12 14.12a3 3 0 11-4.24-4.24"/>
                                    <line x1="1" y1="1" x2="23" y2="23"/>
                                  </svg>
                                </button>
                              </>
                            ) : (
                              <>
                                <span style={{ letterSpacing: '2px', marginRight: '6px' }}>••••</span>
                                <button
                                  onClick={() => handleOpenRevealModal(cfg)}
                                  title={t('hsi.revealPassword')}
                                  style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '0 2px', color: '#007bff', verticalAlign: 'middle' }}
                                >
                                  <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
                                    <path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/>
                                    <circle cx="12" cy="12" r="3"/>
                                  </svg>
                                </button>
                              </>
                            )}
                          </td>
                          <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>
                            <span style={{
                              padding: '2px 8px',
                              borderRadius: '4px',
                              fontWeight: 'bold',
                              backgroundColor: statusInfo.color,
                              color: 'white',
                              fontSize: '12px'
                            }}>
                              {statusInfo.label}
                            </span>
                          </td>
                          <td style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'center' }}>
                            <div style={{ display: 'flex', gap: '4px', justifyContent: 'center', flexWrap: 'wrap' }}>
                              <button
                                onClick={() => handleDeletePppoeRow(cfg.user_id)}
                                disabled={loading}
                                style={{
                                  backgroundColor: '#dc3545',
                                  color: 'white',
                                  border: 'none',
                                  borderRadius: '4px',
                                  padding: '4px 10px',
                                  cursor: loading ? 'not-allowed' : 'pointer',
                                  fontSize: '12px'
                                }}
                              >
                                {t('hsi.deleteAction')}
                              </button>
                              <button
                                onClick={() => handleDialPppoeRow(cfg.user_id)}
                                disabled={loading}
                                style={{
                                  backgroundColor: '#007bff',
                                  color: 'white',
                                  border: 'none',
                                  borderRadius: '4px',
                                  padding: '4px 10px',
                                  cursor: loading ? 'not-allowed' : 'pointer',
                                  fontSize: '12px'
                                }}
                              >
                                {t('hsi.dialAction')}
                              </button>
                              <button
                                onClick={() => handleHangupPppoeRow(cfg.user_id)}
                                disabled={loading}
                                style={{
                                  backgroundColor: '#ffc107',
                                  color: '#000',
                                  border: 'none',
                                  borderRadius: '4px',
                                  padding: '4px 10px',
                                  cursor: loading ? 'not-allowed' : 'pointer',
                                  fontSize: '12px'
                                }}
                              >
                                {t('hsi.hangupAction')}
                              </button>
                              <button
                                onClick={() => handleShowPPPoEInfo(cfg.user_id)}
                                disabled={pppoeInfoMap[cfg.user_id] && pppoeInfoMap[cfg.user_id].loading}
                                style={{
                                  backgroundColor: '#6f42c1',
                                  color: 'white',
                                  border: 'none',
                                  borderRadius: '4px',
                                  padding: '4px 10px',
                                  cursor: (pppoeInfoMap[cfg.user_id] && pppoeInfoMap[cfg.user_id].loading) ? 'not-allowed' : 'pointer',
                                  fontSize: '12px'
                                }}
                              >
                                {pppoeInfoMap[cfg.user_id] && pppoeInfoMap[cfg.user_id].loading
                                  ? t('hsi.pppoeInfoLoading')
                                  : (pppoeInfoMap[cfg.user_id] && pppoeInfoMap[cfg.user_id].expanded ? t('hsi.hidePPPoEInfo') : t('hsi.showPPPoEInfo'))}
                              </button>
                            </div>
                          </td>
                        </tr>
                        {pppoeInfoMap[cfg.user_id] && !pppoeInfoMap[cfg.user_id].loading && pppoeInfoMap[cfg.user_id].expanded && (
                          <tr>
                            <td colSpan={5} style={{ border: '1px solid #dee2e6', padding: '0' }}>
                              <div style={{
                                padding: '12px 16px',
                                backgroundColor: pppoeInfoMap[cfg.user_id].error ? '#f8d7da' : '#f0f7ff',
                                borderTop: '1px solid #dee2e6'
                              }}>
                                {pppoeInfoMap[cfg.user_id].error ? (
                                  <span style={{ color: '#721c24', fontSize: '13px' }}>{pppoeInfoMap[cfg.user_id].error}</span>
                                ) : (
                                  <div>
                                    <strong>{t('hsi.pppoeInfo')} — {t('hsi.user')} {cfg.user_id}</strong>
                                    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '15px', marginTop: '10px' }}>
                                      <div>
                                        <span style={{ color: '#6c757d', fontSize: '13px' }}>
                                          <strong>{t('hsi.pppoeSessionId')}:</strong> {pppoeInfoMap[cfg.user_id].data?.session_id || t('common.notSet')}
                                        </span>
                                      </div>
                                      <div>
                                        <span style={{ color: '#6c757d', fontSize: '13px' }}>
                                          <strong>{t('hsi.pppoeStatus')}:</strong> {pppoeInfoMap[cfg.user_id].data?.status || t('common.notSet')}
                                        </span>
                                      </div>
                                      <div>
                                        <span style={{ color: '#6c757d', fontSize: '13px' }}>
                                          <strong>{t('hsi.pppoeClientIp')}:</strong> {pppoeInfoMap[cfg.user_id].data?.client_ip || t('common.notSet')}
                                        </span>
                                      </div>
                                      <div>
                                        <span style={{ color: '#6c757d', fontSize: '13px' }}>
                                          <strong>{t('hsi.pppoeServerIp')}:</strong> {pppoeInfoMap[cfg.user_id].data?.server_ip || t('common.notSet')}
                                        </span>
                                      </div>
                                      <div style={{ gridColumn: '1 / -1' }}>
                                        <span style={{ color: '#6c757d', fontSize: '13px' }}>
                                          <strong>{t('hsi.pppoeDnsServers')}:</strong> {pppoeInfoMap[cfg.user_id].data?.dns_servers && pppoeInfoMap[cfg.user_id].data.dns_servers.length > 0 ? pppoeInfoMap[cfg.user_id].data.dns_servers.join(', ') : t('common.notSet')}
                                        </span>
                                      </div>
                                    </div>
                                  </div>
                                )}
                              </div>
                            </td>
                          </tr>
                        )}
                      </React.Fragment>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {action === 'snat' && (
        <div>
          <h3>{t('hsi.snatPortForwarding')}</h3>
          <p style={{ color: '#6c757d', marginBottom: '15px', fontSize: '14px' }}>
            {t('hsi.portMappingHint')}
          </p>

          <div style={{ marginBottom: '20px' }}>
            <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.selectUserId')}:</label>
            <select
              value={selectedUserId}
              onChange={(e) => handleUserIdSelect(e.target.value)}
              style={{
                padding: '8px',
                border: '1px solid #ccc',
                borderRadius: '4px',
                minWidth: '200px'
              }}
            >
              <option value="">{t('hsi.selectUserId')}</option>
              {userIds.map(userId => (
                <option key={userId} value={userId}>{userId}</option>
              ))}
            </select>
          </div>

          {selectedUserId && (
            <div style={{ maxWidth: '700px' }}>
              {portMappings.map((pm, idx) => (
                <div key={idx} style={{ 
                  display: 'flex', 
                  gap: '10px', 
                  marginBottom: '10px', 
                  alignItems: 'center',
                  padding: '10px',
                  backgroundColor: '#f8f9fa',
                  borderRadius: '4px',
                  border: '1px solid #dee2e6'
                }}>
                  <span style={{ fontWeight: 'bold', minWidth: '25px', color: '#495057' }}>#{idx + 1}</span>
                  <div style={{ flex: 1 }}>
                    <label style={{ display: 'block', fontSize: '12px', color: '#6c757d', marginBottom: '2px' }}>
                      {t('hsi.portMapping.dip')}
                    </label>
                    <input
                      type="text"
                      placeholder={t('hsi.portMapping.dipPlaceholder')}
                      value={pm.dip}
                      onChange={(e) => updatePortMapping(idx, 'dip', e.target.value)}
                      style={{ width: '100%', padding: '6px', border: '1px solid #ccc', borderRadius: '4px' }}
                    />
                  </div>
                  <div style={{ flex: 1 }}>
                    <label style={{ display: 'block', fontSize: '12px', color: '#6c757d', marginBottom: '2px' }}>
                      {t('hsi.portMapping.dport')}
                    </label>
                    <input
                      type="text"
                      placeholder={t('hsi.portMapping.dportPlaceholder')}
                      value={pm.dport}
                      onChange={(e) => updatePortMapping(idx, 'dport', e.target.value)}
                      style={{ width: '100%', padding: '6px', border: '1px solid #ccc', borderRadius: '4px' }}
                    />
                  </div>
                  <div style={{ flex: 1 }}>
                    <label style={{ display: 'block', fontSize: '12px', color: '#6c757d', marginBottom: '2px' }}>
                      {t('hsi.portMapping.eport')}
                    </label>
                    <input
                      type="text"
                      placeholder={t('hsi.portMapping.eportPlaceholder')}
                      value={pm.eport}
                      onChange={(e) => updatePortMapping(idx, 'eport', e.target.value)}
                      style={{ width: '100%', padding: '6px', border: '1px solid #ccc', borderRadius: '4px' }}
                    />
                  </div>
                  <button
                    onClick={() => removePortMapping(idx)}
                    style={{
                      backgroundColor: '#dc3545',
                      color: 'white',
                      border: 'none',
                      borderRadius: '4px',
                      padding: '6px 10px',
                      cursor: 'pointer',
                      alignSelf: 'flex-end',
                      marginBottom: '2px'
                    }}
                    title={t('common.delete')}
                  >
                    ✕
                  </button>
                </div>
              ))}
              <button
                onClick={addPortMapping}
                style={{
                  backgroundColor: '#17a2b8',
                  color: 'white',
                  border: 'none',
                  borderRadius: '4px',
                  padding: '8px 16px',
                  cursor: 'pointer',
                  marginBottom: '20px'
                }}
              >
                + {t('hsi.portMapping.addRule')}
              </button>
              <div>
                <button
                  onClick={handleSaveSnat}
                  disabled={loading}
                  style={{
                    backgroundColor: '#28a745',
                    color: 'white',
                    border: 'none',
                    borderRadius: '4px',
                    padding: '10px 20px',
                    cursor: loading ? 'not-allowed' : 'pointer'
                  }}
                >
                  {loading ? t('common.processing') : t('hsi.confirm')}
                </button>
              </div>
            </div>
          )}
        </div>
      )}

      {/* ===== Static DNS Record Section ===== */}
      {action === 'dns' && (
        <div>
          <h3>{t('dns.staticDnsRecord')}</h3>
          <p style={{ color: '#6c757d', marginBottom: '15px', fontSize: '14px' }}>
            {t('dns.hint')}
          </p>

          <div style={{ marginBottom: '20px' }}>
            <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.selectUserId')}:</label>
            <select
              value={selectedUserId}
              onChange={(e) => handleUserIdSelect(e.target.value)}
              style={{
                padding: '8px',
                border: '1px solid #ccc',
                borderRadius: '4px',
                minWidth: '200px'
              }}
            >
              <option value="">{t('hsi.selectUserId')}</option>
              {userIds.map(uid => (
                <option key={uid} value={uid}>{uid}</option>
              ))}
            </select>
          </div>

          {selectedUserId && (
            <div style={{ maxWidth: '700px' }}>
              {/* DNS Proxy toggle */}
              <div style={{
                display: 'flex',
                alignItems: 'center',
                gap: '12px',
                padding: '12px 16px',
                backgroundColor: '#f8f9fa',
                borderRadius: '8px',
                border: '1px solid #dee2e6',
                marginBottom: '20px'
              }}>
                <span style={{ fontWeight: '500', fontSize: '14px' }}>{t('hsi.dnsProxy')}</span>
                {dnsTabProxyLoading ? (
                  <span style={{ fontSize: '13px', color: '#6c757d' }}>{t('common.loading')}</span>
                ) : dnsTabProxyEnable !== null ? (
                  <>
                    <button
                      onClick={handleDnsTabToggleProxy}
                      disabled={dnsTabProxyLoading}
                      title={dnsTabProxyEnable ? t('hsi.dnsProxyEnabled') : t('hsi.dnsProxyDisabled')}
                      style={{
                        position: 'relative',
                        display: 'inline-block',
                        width: '44px',
                        height: '24px',
                        borderRadius: '12px',
                        border: 'none',
                        cursor: 'pointer',
                        backgroundColor: dnsTabProxyEnable ? '#28a745' : '#6c757d',
                        transition: 'background-color 0.2s',
                        padding: 0,
                        verticalAlign: 'middle',
                        flexShrink: 0
                      }}
                    >
                      <span style={{
                        position: 'absolute',
                        top: '3px',
                        left: dnsTabProxyEnable ? '23px' : '3px',
                        width: '18px',
                        height: '18px',
                        borderRadius: '50%',
                        backgroundColor: 'white',
                        transition: 'left 0.2s',
                        display: 'block'
                      }} />
                    </button>
                    <span style={{ fontSize: '13px', color: dnsTabProxyEnable ? '#28a745' : '#6c757d', fontWeight: '500' }}>
                      {dnsTabProxyEnable ? t('hsi.dnsProxyEnabled') : t('hsi.dnsProxyDisabled')}
                    </span>
                  </>
                ) : null}
              </div>

              {/* Add / Update DNS record form */}
              <div style={{
                padding: '15px',
                backgroundColor: '#f8f9fa',
                borderRadius: '8px',
                border: '1px solid #dee2e6',
                marginBottom: '20px'
              }}>
                <h4 style={{ marginTop: 0 }}>
                  {dnsIsUpdate ? t('dns.updateRecord') : t('dns.addRecord')}
                  {!dnsIsUpdate && (
                    <span style={{ marginLeft: '10px', color: '#6c757d', fontSize: '12px', fontWeight: 'normal' }}>
                      ({t('dns.maxRecordsHint')})
                    </span>
                  )}
                  {dnsCheckingDomain && (
                    <span style={{ marginLeft: '10px', color: '#007bff', fontSize: '12px' }}>
                      {t('dns.checkingDomain')}
                    </span>
                  )}
                </h4>
                {dnsIsUpdate && (
                  <div style={{
                    padding: '8px 12px',
                    backgroundColor: '#fff3cd',
                    borderRadius: '4px',
                    border: '1px solid #ffc107',
                    marginBottom: '12px',
                    fontSize: '13px',
                    color: '#856404'
                  }}>
                    {t('dns.updateNotice')}
                  </div>
                )}
                <div style={{ display: 'flex', gap: '10px', flexWrap: 'wrap', alignItems: 'flex-end' }}>
                  <div style={{ flex: 2, minWidth: '180px' }}>
                    <label style={{ display: 'block', fontSize: '12px', color: '#6c757d', marginBottom: '4px' }}>
                      {t('dns.domain')}
                    </label>
                    <input
                      type="text"
                      placeholder={t('dns.domainPlaceholder')}
                      value={dnsForm.domain}
                      onChange={(e) => handleDnsDomainChange(e.target.value)}
                      style={{ width: '100%', padding: '8px', border: '1px solid #ccc', borderRadius: '4px' }}
                    />
                  </div>
                  <div style={{ flex: 1, minWidth: '140px' }}>
                    <label style={{ display: 'block', fontSize: '12px', color: '#6c757d', marginBottom: '4px' }}>
                      {t('dns.ip')}
                    </label>
                    <input
                      type="text"
                      placeholder={t('dns.ipPlaceholder')}
                      value={dnsForm.ip}
                      onChange={(e) => setDnsForm(prev => ({ ...prev, ip: e.target.value }))}
                      style={{ width: '100%', padding: '8px', border: '1px solid #ccc', borderRadius: '4px' }}
                    />
                  </div>
                  <div style={{ flex: 1, minWidth: '80px' }}>
                    <label style={{ display: 'block', fontSize: '12px', color: '#6c757d', marginBottom: '4px' }}>
                      {t('dns.ttl')}
                    </label>
                    <input
                      type="number"
                      min="1"
                      placeholder={t('dns.ttlPlaceholder')}
                      value={dnsForm.ttl}
                      onChange={(e) => setDnsForm(prev => ({ ...prev, ttl: e.target.value }))}
                      style={{ width: '100%', padding: '8px', border: '1px solid #ccc', borderRadius: '4px' }}
                    />
                  </div>
                  <button
                    onClick={handleAddOrUpdateDns}
                    disabled={loading || dnsCheckingDomain}
                    style={{
                      backgroundColor: dnsIsUpdate ? '#ffc107' : '#28a745',
                      color: dnsIsUpdate ? '#000' : 'white',
                      border: 'none',
                      borderRadius: '4px',
                      padding: '8px 16px',
                      cursor: (loading || dnsCheckingDomain) ? 'not-allowed' : 'pointer',
                      whiteSpace: 'nowrap',
                      alignSelf: 'flex-end'
                    }}
                  >
                    {loading ? t('common.processing') : (dnsIsUpdate ? t('dns.update') : t('dns.add'))}
                  </button>
                </div>
              </div>

              {/* DNS records table */}
              <h4>{t('dns.currentRecords')}</h4>
              {dnsLoading ? (
                <div style={{ textAlign: 'center', padding: '15px' }}>{t('common.loading')}</div>
              ) : dnsRecords.length === 0 ? (
                <div style={{ color: '#6c757d', marginBottom: '10px' }}>{t('dns.noRecords')}</div>
              ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: '10px' }}>
                  <thead>
                    <tr style={{ backgroundColor: '#f8f9fa' }}>
                      <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('dns.domain')}</th>
                      <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('dns.ip')}</th>
                      <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('dns.ttl')}</th>
                      <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'center' }}>{t('hsi.actions')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {dnsRecords.map((rec, idx) => (
                      <tr key={idx}>
                        <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{rec.domain}</td>
                        <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{rec.ip}</td>
                        <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{rec.ttl}</td>
                        <td style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'center' }}>
                          <button
                            onClick={() => handleDeleteDns(rec.domain)}
                            disabled={loading}
                            style={{
                              backgroundColor: '#dc3545',
                              color: 'white',
                              border: 'none',
                              borderRadius: '4px',
                              padding: '4px 10px',
                              cursor: loading ? 'not-allowed' : 'pointer',
                              fontSize: '12px'
                            }}
                          >
                            {t('common.delete')}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}
        </div>
      )}

      {/* ===== ARP Table Section ===== */}
      {action === 'arp' && (
        <div>
          <h3>{t('hsi.arpTable')}</h3>
          <p style={{ color: '#6c757d', marginBottom: '15px', fontSize: '14px' }}>
            {t('hsi.arpTableHint') || 'View ARP table entries for the selected user'}
          </p>

          <div style={{ marginBottom: '20px' }}>
            <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.selectUserId')}:</label>
            <select
              value={selectedUserId}
              onChange={(e) => handleUserIdSelect(e.target.value)}
              style={{
                padding: '8px',
                border: '1px solid #ccc',
                borderRadius: '4px',
                minWidth: '200px'
              }}
            >
              <option value="">{t('hsi.selectUserId')}</option>
              {userIds.map(uid => (
                <option key={uid} value={uid}>{uid}</option>
              ))}
            </select>
          </div>

          {selectedUserId && (
            <div>
              <div style={{ marginBottom: '20px' }}>
                <button
                  onClick={() => loadArpTable(selectedUserId)}
                  disabled={arpTableLoading}
                  style={{
                    backgroundColor: '#007bff',
                    color: 'white',
                    border: 'none',
                    borderRadius: '4px',
                    padding: '10px 20px',
                    cursor: arpTableLoading ? 'not-allowed' : 'pointer'
                  }}
                >
                  {arpTableLoading ? t('common.loading') : t('hsi.refreshArpTable') || 'Refresh ARP Table'}
                </button>
              </div>

              {arpTableLoading && (
                <div style={{ textAlign: 'center', padding: '15px' }}>{t('common.loading')}</div>
              )}

              {arpTableError && (
                <div style={{
                  backgroundColor: '#f8d7da',
                  color: '#721c24',
                  padding: '12px',
                  borderRadius: '4px',
                  marginBottom: '20px'
                }}>
                  {arpTableError}
                </div>
              )}

              {!arpTableLoading && arpTableData && !arpTableError && (
                <div>
                  <h4>{t('hsi.arpTableInfo')} — {t('hsi.user')} {selectedUserId} ({arpTableData.total_count} {t('hsi.entries')})</h4>
                  {arpTableData.entries && arpTableData.entries.length > 0 ? (
                    <div style={{ overflowX: 'auto' }}>
                      <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: '10px' }}>
                        <thead>
                          <tr style={{ backgroundColor: '#f8f9fa' }}>
                            <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>Table ID</th>
                            <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>IP</th>
                            <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>MAC</th>
                          </tr>
                        </thead>
                        <tbody>
                          {arpTableData.entries.slice(0, 50).map((entry, i) => (
                            <tr key={i}>
                              <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{entry.entry_id}</td>
                              <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{entry.ip}</td>
                              <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{entry.mac}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                      {arpTableData.entries.length > 50 && (
                        <div style={{ color: '#6c757d', fontSize: '13px' }}>
                          {t('hsi.showing')} 50 {t('hsi.of')} {arpTableData.total_count}
                        </div>
                      )}
                    </div>
                  ) : (
                    <div style={{ color: '#6c757d', marginTop: '10px' }}>
                      {t('hsi.arpTableEmpty')}
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {/* ===== DNS Cache Section ===== */}
      {action === 'dns-cache' && (
        <div>
          <h3>{t('hsi.dnsCache')}</h3>
          <p style={{ color: '#6c757d', marginBottom: '15px', fontSize: '14px' }}>
            {t('hsi.dnsCacheHint') || 'View DNS cache entries for the selected user'}
          </p>

          <div style={{ marginBottom: '20px' }}>
            <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.selectUserId')}:</label>
            <select
              value={selectedUserId}
              onChange={(e) => handleUserIdSelect(e.target.value)}
              style={{
                padding: '8px',
                border: '1px solid #ccc',
                borderRadius: '4px',
                minWidth: '200px'
              }}
            >
              <option value="">{t('hsi.selectUserId')}</option>
              {userIds.map(uid => (
                <option key={uid} value={uid}>{uid}</option>
              ))}
            </select>
          </div>

          {selectedUserId && (
            <div>
              <div style={{ marginBottom: '20px' }}>
                <button
                  onClick={() => loadDnsCache(selectedUserId)}
                  disabled={dnsCacheLoading}
                  style={{
                    backgroundColor: '#007bff',
                    color: 'white',
                    border: 'none',
                    borderRadius: '4px',
                    padding: '10px 20px',
                    cursor: dnsCacheLoading ? 'not-allowed' : 'pointer'
                  }}
                >
                  {dnsCacheLoading ? t('common.loading') : t('hsi.refreshDnsCache') || 'Refresh DNS Cache'}
                </button>
              </div>

              {dnsCacheLoading && (
                <div style={{ textAlign: 'center', padding: '15px' }}>{t('common.loading')}</div>
              )}

              {dnsCacheError && (
                <div style={{
                  backgroundColor: '#f8d7da',
                  color: '#721c24',
                  padding: '12px',
                  borderRadius: '4px',
                  marginBottom: '20px'
                }}>
                  {dnsCacheError}
                </div>
              )}

              {!dnsCacheLoading && dnsCacheData && !dnsCacheError && (
                <div>
                  <h4>{t('hsi.dnsCacheInfo')} — {t('hsi.user')} {selectedUserId} ({dnsCacheData.total_count} {t('hsi.entries')})</h4>
                  {dnsCacheData.entries && dnsCacheData.entries.length > 0 ? (
                    <div style={{ overflowX: 'auto' }}>
                      <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: '10px' }}>
                        <thead>
                          <tr style={{ backgroundColor: '#f8f9fa' }}>
                            <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.dnsDomain')}</th>
                            <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>QType</th>
                            <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>TTL</th>
                            <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.dnsRemaining')}</th>
                            <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.dnsHitCount')}</th>
                          </tr>
                        </thead>
                        <tbody>
                          {dnsCacheData.entries.slice(0, 50).map((entry, i) => (
                            <tr key={i}>
                              <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{entry.domain}</td>
                              <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{entry.qtype}</td>
                              <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{entry.ttl}</td>
                              <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{entry.remaining_ttl}</td>
                              <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{entry.hit_count}</td>
                            </tr>
                          ))}
                        </tbody>
                      </table>
                      {dnsCacheData.entries.length > 50 && (
                        <div style={{ color: '#6c757d', fontSize: '13px' }}>
                          {t('hsi.showing')} 50 {t('hsi.of')} {dnsCacheData.total_count}
                        </div>
                      )}
                    </div>
                  ) : (
                    <div style={{ color: '#6c757d', marginTop: '10px' }}>
                      {t('hsi.dnsCacheEmpty')}
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {/* ===== Other Switches Section ===== */}
      {action === 'switches' && (
        <div>
          <h3>{t('hsi.otherSwitches')}</h3>

          <div style={{ marginBottom: '20px' }}>
            <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.selectUserId')}:</label>
            <select
              value={selectedUserId}
              onChange={(e) => handleUserIdSelect(e.target.value)}
              style={{ padding: '8px', border: '1px solid #ccc', borderRadius: '4px', minWidth: '200px' }}
            >
              <option value="">{t('hsi.selectUserId')}</option>
              {userIds.map(uid => (
                <option key={uid} value={uid}>{uid}</option>
              ))}
            </select>
          </div>

          {selectedUserId && (
            <div style={{ maxWidth: '500px' }}>
              <div style={{
                padding: '16px',
                backgroundColor: '#f8f9fa',
                borderRadius: '8px',
                border: '1px solid #dee2e6'
              }}>
                {/* TCP Conntrack toggle row */}
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px', padding: '8px 0' }}>
                  <span style={{ flex: 1, fontWeight: '500', fontSize: '14px' }}>{t('hsi.tcpConntrack')}</span>
                  {switchesLoading ? (
                    <span style={{ fontSize: '13px', color: '#6c757d' }}>{t('common.loading')}</span>
                  ) : tcpConntrackEnable !== null ? (
                    <>
                      <button
                        onClick={handleToggleTcpConntrack}
                        disabled={switchesLoading}
                        title={tcpConntrackEnable ? t('hsi.tcpConntrackEnabled') : t('hsi.tcpConntrackDisabled')}
                        style={{
                          position: 'relative',
                          display: 'inline-block',
                          width: '44px',
                          height: '24px',
                          borderRadius: '12px',
                          border: 'none',
                          cursor: 'pointer',
                          backgroundColor: tcpConntrackEnable ? '#28a745' : '#6c757d',
                          transition: 'background-color 0.2s',
                          padding: 0,
                          verticalAlign: 'middle',
                          flexShrink: 0
                        }}
                      >
                        <span style={{
                          position: 'absolute',
                          top: '3px',
                          left: tcpConntrackEnable ? '23px' : '3px',
                          width: '18px',
                          height: '18px',
                          borderRadius: '50%',
                          backgroundColor: 'white',
                          transition: 'left 0.2s',
                          display: 'block'
                        }} />
                      </button>
                      <span style={{ fontSize: '13px', color: tcpConntrackEnable ? '#28a745' : '#6c757d', fontWeight: '500', minWidth: '30px' }}>
                        {tcpConntrackEnable ? t('hsi.tcpConntrackEnabled') : t('hsi.tcpConntrackDisabled')}
                      </span>
                    </>
                  ) : null}
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      {/* ===== DHCP Server Section ===== */}
      {action === 'dhcp-server' && (
        <div>
          <h3>{t('hsi.dhcpServer')}</h3>
          <p style={{ color: '#6c757d', marginBottom: '15px', fontSize: '14px' }}>
            {t('hsi.dhcpServerHint') || 'View DHCP server configuration for the selected user'}
          </p>

          <div style={{ marginBottom: '20px' }}>
            <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.selectUserId')}:</label>
            <select
              value={selectedUserId}
              onChange={(e) => handleUserIdSelect(e.target.value)}
              style={{
                padding: '8px',
                border: '1px solid #ccc',
                borderRadius: '4px',
                minWidth: '200px'
              }}
            >
              <option value="">{t('hsi.selectUserId')}</option>
              {userIds.map(uid => (
                <option key={uid} value={uid}>{uid}</option>
              ))}
            </select>
          </div>

          {selectedUserId && (
            <div>
              <div style={{ marginBottom: '20px' }}>
                <button
                  onClick={() => loadDhcpConfig(selectedUserId)}
                  disabled={dhcpConfigLoading}
                  style={{
                    backgroundColor: '#007bff',
                    color: 'white',
                    border: 'none',
                    borderRadius: '4px',
                    padding: '10px 20px',
                    cursor: dhcpConfigLoading ? 'not-allowed' : 'pointer'
                  }}
                >
                  {dhcpConfigLoading ? t('common.loading') : t('hsi.refreshDhcpConfig') || 'Refresh DHCP Config'}
                </button>
              </div>

              {dhcpConfigLoading && (
                <div style={{ textAlign: 'center', padding: '15px' }}>{t('common.loading')}</div>
              )}

              {dhcpConfigError && (
                <div style={{
                  backgroundColor: '#f8d7da',
                  color: '#721c24',
                  padding: '12px',
                  borderRadius: '4px',
                  marginBottom: '20px'
                }}>
                  {dhcpConfigError}
                </div>
              )}

              {!dhcpConfigLoading && dhcpConfigData && !dhcpConfigError && (
                <div style={{
                  padding: '15px',
                  backgroundColor: '#f8f9fa',
                  borderRadius: '8px',
                  border: '1px solid #dee2e6'
                }}>
                  <h4 style={{ marginTop: 0 }}>{t('hsi.dhcpConfig')} — {t('hsi.user')} {selectedUserId}</h4>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '15px', marginBottom: '20px' }}>
                    <div>
                      <strong>{t('hsi.dhcpStatus')}:</strong><br/>
                      <span style={{ color: '#6c757d' }}>{dhcpConfigData.status || t('common.notSet')}</span>
                    </div>
                    <div>
                      <strong>{t('hsi.dhcpAddrPoolLabel')}:</strong><br/>
                      <span style={{ color: '#6c757d' }}>{dhcpConfigData.ip_range || t('common.notSet')}</span>
                    </div>
                    <div>
                      <strong>{t('hsi.subnetLabel')}:</strong><br/>
                      <span style={{ color: '#6c757d' }}>{dhcpConfigData.subnet_mask || t('common.notSet')}</span>
                    </div>
                    <div>
                      <strong>{t('hsi.gatewayLabel')}:</strong><br/>
                      <span style={{ color: '#6c757d' }}>{dhcpConfigData.gateway || t('common.notSet')}</span>
                    </div>
                    <div style={{ gridColumn: '1 / -1' }}>
                      <strong>{t('hsi.dhcpLeaseUsage')}:</strong><br/>
                      <span style={{ color: '#6c757d' }}>
                        {dhcpConfigData.cur_lease_count} / {dhcpConfigData.max_lease_count}
                        {dhcpConfigData.max_lease_count > 0 && ` (${Math.round((dhcpConfigData.cur_lease_count / dhcpConfigData.max_lease_count) * 100)}%)`}
                      </span>
                    </div>
                    {dhcpConfigData.inuse_ips && dhcpConfigData.inuse_ips.length > 0 && (
                      <div style={{ gridColumn: '1 / -1' }}>
                        <strong>{t('hsi.dhcpInuseIpsLabel')}:</strong><br/>
                        <span style={{ color: '#6c757d', fontSize: '12px' }}>
                          {dhcpConfigData.inuse_ips.join(', ')}
                        </span>
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {loading && !action && (
        <div style={{ textAlign: 'center', padding: '20px' }}>
          {t('common.loading')}
        </div>
      )}

      {/* ===== Reveal Password Modal ===== */}
      {revealModal.open && (
        <div style={{
          position: 'fixed',
          top: 0, left: 0, right: 0, bottom: 0,
          backgroundColor: 'rgba(0,0,0,0.5)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          zIndex: 1000
        }}>
          <div style={{
            backgroundColor: 'white',
            borderRadius: '8px',
            padding: '24px',
            minWidth: '340px',
            maxWidth: '420px',
            boxShadow: '0 4px 20px rgba(0,0,0,0.3)'
          }}>
            <h3 style={{ marginTop: 0, marginBottom: '8px' }}>
              {t('hsi.revealPasswordModal.title')}
            </h3>
            <p style={{ color: '#6c757d', fontSize: '14px', marginBottom: '16px' }}>
              {t('hsi.revealPasswordModal.hint')}
              {revealModal.userId && (
                <span style={{ fontWeight: 'bold' }}> (User ID: {revealModal.userId})</span>
              )}
            </p>

            <div>
              <div style={{ marginBottom: '14px' }}>
                <label style={{ display: 'block', marginBottom: '5px', fontSize: '14px' }}>
                  {t('hsi.revealPasswordModal.adminPasswordLabel')}
                </label>
                <input
                  type="password"
                  value={revealModal.adminPassword}
                  onChange={(e) => setRevealModal(prev => ({ ...prev, adminPassword: e.target.value, error: '' }))}
                  onKeyDown={(e) => { if (e.key === 'Enter') handleVerifyAdminPassword() }}
                  autoFocus
                  style={{
                    width: '100%',
                    padding: '8px',
                    border: revealModal.error ? '2px solid #dc3545' : '1px solid #ccc',
                    borderRadius: '4px',
                    boxSizing: 'border-box'
                  }}
                />
                {revealModal.error && (
                  <div style={{ color: '#dc3545', fontSize: '13px', marginTop: '4px' }}>
                    {revealModal.error}
                  </div>
                )}
              </div>
              <div style={{ display: 'flex', gap: '10px', justifyContent: 'flex-end' }}>
                <button
                  onClick={handleCloseRevealModal}
                  style={{
                    backgroundColor: '#6c757d',
                    color: 'white',
                    border: 'none',
                    borderRadius: '4px',
                    padding: '8px 16px',
                    cursor: 'pointer'
                  }}
                >
                  {t('hsi.revealPasswordModal.cancel')}
                </button>
                <button
                  onClick={handleVerifyAdminPassword}
                  disabled={revealModal.loading || !revealModal.adminPassword}
                  style={{
                    backgroundColor: '#007bff',
                    color: 'white',
                    border: 'none',
                    borderRadius: '4px',
                    padding: '8px 16px',
                    cursor: (revealModal.loading || !revealModal.adminPassword) ? 'not-allowed' : 'pointer'
                  }}
                >
                  {revealModal.loading ? t('common.processing') : t('hsi.revealPasswordModal.submit')}
                </button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
