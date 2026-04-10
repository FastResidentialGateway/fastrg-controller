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
  getDnsRecords,
  getDnsRecord,
  addOrUpdateDnsRecord,
  deleteDnsRecord
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
    if (action === 'list' || action === 'delete' || action === 'dial' || action === 'hangup' || action === 'snat' || action === 'dns') {
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

  const handleActionChange = (selectedAction) => {
    setAction(selectedAction)
    setError(null)
    setCurrentStep(1)
    setPppoeConfig({
      user_id: '',
      vlan_id: '',
      account_name: '',
      password: ''
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
      // Load DNS records for this user
      loadDnsRecords(userId)
    }
  }

  const handleInputChange = async (field, value) => {
    if (currentStep === 1) {
      setPppoeConfig(prev => ({
        ...prev,
        [field]: value
      }))

      // Check and auto-fill existing config for user_id while creating new config
      if (field === 'user_id' && action === 'create') {
        // Create previous timeout
        if (autoFillTimeout) {
          clearTimeout(autoFillTimeout)
        }

        // Stop checking if input is cleared
        if (value.trim() === '') {
          setIsCheckingConfig(false)
          return
        }

        // Set new timeout (500ms delay)
        const timeoutId = setTimeout(async () => {
          await checkAndAutoFillConfig(value.trim())
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
        password: ''
      })
      setDhcpConfig({
        dhcp_addr_pool: '',
        dhcp_subnet: '',
        dhcp_gateway: ''
      })
      setPortMappings([])
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
      const msg = extractApiError(err) || t('dns.saveFailed')
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
            { key: 'create', labelKey: 'hsi.createPppoe' },
            { key: 'list', labelKey: 'hsi.listPppoe' },
            { key: 'delete', labelKey: 'hsi.deletePppoe' },
            { key: 'dial', labelKey: 'hsi.dial' },
            { key: 'hangup', labelKey: 'hsi.hangup' },
            { key: 'snat', labelKey: 'hsi.snatPortForwarding' },
            { key: 'dns', labelKey: 'dns.staticDnsRecord' }
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

      {/* Present corresponding UI for the action user selected */}
      {action === 'create' && (
        <div>
          {currentStep === 1 ? (
            <div>
              <h3>{t('hsi.step1AddPppoe')}</h3>
              <div style={{ maxWidth: '400px' }}>
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>
                    {t('hsi.userId')} (1-2000):
                    {isCheckingConfig && (
                      <span style={{ 
                        marginLeft: '10px',
                        color: '#007bff',
                        fontSize: '12px'
                      }}>
                        {t('hsi.checkingConfig')}
                      </span>
                    )}
                  </label>
                  {hasFieldError('user_id') && (
                    <div style={{ 
                      color: '#dc3545', 
                      fontSize: '12px', 
                      marginBottom: '3px' 
                    }}>
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
                    <div style={{ 
                      color: '#dc3545', 
                      fontSize: '12px', 
                      marginBottom: '3px' 
                    }}>
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
                    <div style={{ 
                      color: '#dc3545', 
                      fontSize: '12px', 
                      marginBottom: '3px' 
                    }}>
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
                    <div style={{ 
                      color: '#dc3545', 
                      fontSize: '12px', 
                      marginBottom: '3px' 
                    }}>
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
                    backgroundColor: '#007bff',
                    color: 'white',
                    border: 'none',
                    borderRadius: '4px',
                    padding: '10px 20px',
                    cursor: loading ? 'not-allowed' : 'pointer'
                  }}
                >
                  {loading ? t('common.processing') : t('hsi.nextStepDhcp')}
                </button>
              </div>
            </div>
          ) : (
            <div>
              <h3>{t('hsi.step2Dhcp')}</h3>
              <div style={{ maxWidth: '400px' }}>
                <div style={{ marginBottom: '15px' }}>
                  <label style={{ display: 'block', marginBottom: '5px' }}>{t('hsi.dhcpAddrPoolLabel')}:</label>
                  {hasFieldError('dhcp_addr_pool') && (
                    <div style={{ 
                      color: '#dc3545', 
                      fontSize: '12px', 
                      marginBottom: '3px' 
                    }}>
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
                    <div style={{ 
                      color: '#dc3545', 
                      fontSize: '12px', 
                      marginBottom: '3px' 
                    }}>
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
                    <div style={{ 
                      color: '#dc3545', 
                      fontSize: '12px', 
                      marginBottom: '3px' 
                    }}>
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
                      // Clear field validation states when going to first step
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
                      backgroundColor: '#007bff',
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

      {(action === 'list' || action === 'delete' || action === 'dial' || action === 'hangup') && (
        <div>
          <h3>
            {action === 'list' && t('hsi.listPppoe')}
            {action === 'delete' && t('hsi.deletePppoe')}
            {action === 'dial' && t('hsi.dial')}
            {action === 'hangup' && t('hsi.hangup')}
          </h3>

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

          {action === 'list' && selectedUserId && pppoeConfig.user_id && (
            <div style={{ maxWidth: '600px' }}>
              <h4>{t('hsi.pppoeDetails')}</h4>
              <div style={{ marginBottom: '10px' }}>
                <strong>{t('hsi.userId')}:</strong> {pppoeConfig.user_id}
              </div>
              <div style={{ marginBottom: '10px' }}>
                <strong>{t('hsi.vlanLabel')}:</strong> {pppoeConfig.vlan_id}
              </div>
              <div style={{ marginBottom: '10px' }}>
                <strong>{t('hsi.accountNameLabel')}:</strong> {pppoeConfig.account_name}
              </div>
              <div style={{ marginBottom: '10px' }}>
                <strong>{t('hsi.password')}:</strong> {'*'.repeat(pppoeConfig.password.length)}
              </div>
              <div style={{ marginBottom: '20px' }}>
                <strong>{t('hsi.status')}:</strong>{' '}
                {(() => {
                  const info = getStatusInfo(pppoeConfig.enableStatus)
                  return (
                    <span style={{
                      padding: '4px 12px',
                      borderRadius: '4px',
                      fontWeight: 'bold',
                      backgroundColor: info.color,
                      color: 'white'
                    }}>
                      {info.label}
                    </span>
                  )
                })()}
              </div>

              <h4>{t('hsi.dhcpDetails')}</h4>
              <div style={{ marginBottom: '10px' }}>
                <strong>{t('hsi.dhcpAddrPoolLabel')}:</strong> {dhcpConfig.dhcp_addr_pool || t('common.notSet')}
              </div>
              <div style={{ marginBottom: '10px' }}>
                <strong>{t('hsi.subnetLabel')}:</strong> {dhcpConfig.dhcp_subnet || t('common.notSet')}
              </div>
              <div style={{ marginBottom: '10px' }}>
                <strong>{t('hsi.gatewayLabel')}:</strong> {dhcpConfig.dhcp_gateway || t('common.notSet')}
              </div>

              <h4>{t('hsi.portMappingDetails')}</h4>
              {portMappings.length === 0 ? (
                <div style={{ marginBottom: '10px', color: '#6c757d' }}>
                  {t('hsi.portMapping.noRules')}
                </div>
              ) : (
                <table style={{ width: '100%', borderCollapse: 'collapse', marginBottom: '10px' }}>
                  <thead>
                    <tr style={{ backgroundColor: '#f8f9fa' }}>
                      <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>#</th>
                      <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.portMapping.dip')}</th>
                      <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.portMapping.dport')}</th>
                      <th style={{ border: '1px solid #dee2e6', padding: '8px', textAlign: 'left' }}>{t('hsi.portMapping.eport')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {portMappings.map((pm, idx) => (
                      <tr key={idx}>
                        <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{pm.index || idx}</td>
                        <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{pm.dip}</td>
                        <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{pm.dport}</td>
                        <td style={{ border: '1px solid #dee2e6', padding: '8px' }}>{pm.eport}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          )}

          {(action === 'delete' || action === 'dial' || action === 'hangup') && (
            <button
              onClick={
                action === 'delete' ? handleDelete :
                action === 'dial' ? handleDial :
                handleHangup
              }
              disabled={loading || !selectedUserId}
              style={{
                backgroundColor: 
                  action === 'delete' ? '#dc3545' :
                  action === 'dial' ? '#007bff' : '#ffc107',
                color: action === 'hangup' ? '#000' : 'white',
                border: 'none',
                borderRadius: '4px',
                padding: '10px 20px',
                cursor: (loading || !selectedUserId) ? 'not-allowed' : 'pointer'
              }}
            >
              {loading ? t('common.processing') : 
                action === 'delete' ? t('hsi.confirmDeleteAction') :
                action === 'dial' ? t('hsi.confirmDialAction') : t('hsi.confirmHangupAction')
              }
            </button>
          )}
        </div>
      )}

      {loading && !action && (
        <div style={{ textAlign: 'center', padding: '20px' }}>
          {t('common.loading')}
        </div>
      )}
    </div>
  )
}
