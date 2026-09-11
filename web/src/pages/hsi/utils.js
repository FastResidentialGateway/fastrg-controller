// The backend rejects a user id above the node's subscriber count with this
// exact message; the UI shows its own wording for it.
export const USER_ID_EXCEEDS = 'User ID exceeds subscriber count'

export function extractApiError(err) {
  try {
    return (err && err.response && err.response.data && err.response.data.error) || err.message || String(err)
  } catch (_) {
    return String(err)
  }
}

// Config fields every write must carry. ipv6_enable is deliberately absent:
// only the IPv6 toggle and the PPPoE form send it, so the other writes leave
// the stored value alone.
export function baseConfigPayload(configData, userId) {
  return {
    user_id: configData.user_id || userId,
    vlan_id: configData.vlan_id || '',
    account_name: configData.account_name || '',
    password: configData.password || '',
    dns_proxy_enable: configData.dns_proxy_enable !== undefined ? configData.dns_proxy_enable : true,
    tcp_conntrack_enable: configData.tcp_conntrack_enable !== undefined ? configData.tcp_conntrack_enable : true,
    dhcp_addr_pool: configData.dhcp_addr_pool || '',
    dhcp_subnet: configData.dhcp_subnet || '',
    dhcp_gateway: configData.dhcp_gateway || ''
  }
}

// Carry the stored port mappings through a write that does not edit them.
export function withPortMapping(payload, configData) {
  if (Array.isArray(configData['port-mapping']) && configData['port-mapping'].length > 0) {
    return { ...payload, 'port-mapping': configData['port-mapping'] }
  }
  return payload
}

// A REST response is either the config itself or { config, metadata }.
export function unwrapConfig(response) {
  return response.config || response
}
