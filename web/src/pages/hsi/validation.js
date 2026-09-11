// Field rules for the HSI forms. Each returns an error message, or null when
// the values are acceptable.

export function validatePPPoEConfig(pppoeConfig, t) {
  const { user_id, vlan_id, account_name, password } = pppoeConfig

  if (!user_id) return t('hsi.error.missingUserId')
  if (!vlan_id) return t('hsi.error.missingVlan')
  if (!account_name) return t('hsi.error.missingAccountName')
  if (!password) return t('hsi.error.missingPassword')

  const userIdNum = parseInt(user_id)
  if (isNaN(userIdNum) || userIdNum < 1 || userIdNum > 2000) {
    return t('hsi.error.userIdRange')
  }

  const vlanIdNum = parseInt(vlan_id)
  if (isNaN(vlanIdNum) || vlanIdNum < 2 || vlanIdNum > 4000) {
    return t('hsi.error.vlanRange')
  }

  return null
}

export function validateDHCPConfig(dhcpConfig, t) {
  const { dhcp_addr_pool, dhcp_subnet, dhcp_gateway } = dhcpConfig

  if (!dhcp_addr_pool) return t('hsi.error.missingDhcpPool')
  if (!dhcp_subnet) return t('hsi.error.missingSubnet')
  if (!dhcp_gateway) return t('hsi.error.missingGateway')

  // Address pool must be written as 'IP~IP' or 'IP-IP'
  const poolMatch = dhcp_addr_pool.match(/^(\d+\.\d+\.\d+\.\d+)[~-](\d+\.\d+\.\d+\.\d+)$/)
  if (!poolMatch) {
    return t('hsi.error.invalidDhcpPoolFormat')
  }

  const startIP = poolMatch[1]
  const endIP = poolMatch[2]

  const isPrivateIP = (ip) => {
    const parts = ip.split('.').map(Number)
    return (parts[0] === 10) ||
           (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) ||
           (parts[0] === 192 && parts[1] === 168)
  }

  if (!isPrivateIP(startIP) || !isPrivateIP(endIP)) {
    return t('hsi.error.dhcpPoolPrivateIp')
  }

  if (startIP.endsWith('.0') || startIP.endsWith('.255') ||
      endIP.endsWith('.0') || endIP.endsWith('.255')) {
    return t('hsi.error.dhcpPoolBadEnd')
  }

  const subnetParts = dhcp_subnet.split('.').map(Number)
  if (subnetParts.length !== 4 || subnetParts.some(part => isNaN(part) || part < 0 || part > 255)) {
    return t('hsi.error.invalidSubnetMask')
  }

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

  if (gatewayParts.length !== 4 || gatewayParts.some(part => isNaN(part) || part < 0 || part > 255)) {
    return t('hsi.error.invalidGateway')
  }

  if (dhcp_gateway.endsWith('.0')) {
    return t('hsi.error.gatewayEndsZero')
  }

  const sameSubnet = startParts.every((part, index) => {
    const mask = subnetParts[index]
    return (part & mask) === (gatewayParts[index] & mask)
  })

  if (!sameSubnet) {
    return t('hsi.error.gatewayNotSameSubnet')
  }

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

export function validatePortMappings(portMappings, t) {
  for (let i = 0; i < portMappings.length; i++) {
    const pm = portMappings[i]
    if (!pm.dip || pm.dip.trim() === '') {
      return t('hsi.error.portMapping.missingDip').replace('{index}', String(i + 1))
    }
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

// Same shape check the DNS form applies before sending a record.
export function validateDnsRecord({ domain, ip, ttl }, t) {
  if (!domain.trim()) return t('dns.error.missingDomain')
  if (!ip.trim()) return t('dns.error.missingIp')
  const ttlNum = parseInt(ttl, 10)
  if (isNaN(ttlNum) || ttlNum <= 0) return t('dns.error.invalidTtl')
  const ipParts = ip.split('.').map(Number)
  if (ipParts.length !== 4 || ipParts.some(p => isNaN(p) || p < 0 || p > 255)) {
    return t('dns.error.invalidIp')
  }
  return null
}
