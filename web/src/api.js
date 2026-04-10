import axios from 'axios'

// Set axios to support HTTPS in development environment
axios.defaults.timeout = 10000
if (process.env.NODE_ENV === 'development') {
  // Accept self-signed certificates in development
  axios.defaults.httpsAgent = new (require('https').Agent)({
    rejectUnauthorized: false
  })
}

// Add response interceptor to handle authentication errors
axios.interceptors.response.use(
  (response) => {
    // If the response is successful, just return it
    return response
  },
  (error) => {
    // Check if the error is due to authentication issues
    if (error.response && (error.response.status === 401 || error.response.status === 403)) {
      // Check if the error message indicates token issues
      const errorMessage = error.response.data?.error || ''
      const isTokenError = errorMessage.includes('Invalid token') || 
                          errorMessage.includes('Missing Authorization') || 
                          errorMessage.includes('Token has been revoked') ||
                          errorMessage.includes('token') ||
                          error.response.status === 401

      if (isTokenError) {
        console.warn('Authentication failed: Token expired or invalid, redirecting to login')

        // Clear the invalid token
        localStorage.removeItem('token')

        // Show a user-friendly message
        if (window.location.pathname !== '/' && window.location.pathname !== '/register') {
          // Only show alert if we're not already on login page
          setTimeout(() => {
            alert('您的登入已過期，請重新登入')
          }, 100)
        }

        // Redirect to login page
        // Use window.location to ensure we can redirect from anywhere
        if (window.location.pathname !== '/' && window.location.pathname !== '/register') {
          window.location.href = '/'
        }

        // Dispatch a custom event to notify components about logout
        window.dispatchEvent(new CustomEvent('auth:logout', { 
          detail: { reason: 'token_expired' }
        }))
      }
    }

    // Re-throw the error so it can still be handled by the calling code
    return Promise.reject(error)
  }
)

export async function apiLogin(username, password){
  try {
    const resp = await axios.post('/api/login', { username, password })
    if(resp.status !== 200) throw new Error('login failed')
    return resp.data.token
  } catch (error) {
    // Re-throw the error with the response intact so Login.jsx can check the status
    throw error
  }
}

export async function apiRegister(username, password){
  const resp = await axios.post('/api/register', { username, password })
  if(resp.status !== 200) throw new Error('registration failed')
  return resp.data
}

export async function fetchNodes(){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.get('/api/nodes', { headers })
  if(resp.status !== 200) throw new Error('failed to fetch nodes')

  // backend may return array of {key,value} when reading etcd; try to parse value JSON
  const data = resp.data
  // try to convert kv list where value might be JSON
  let parsed = null
  if (Array.isArray(data) && data.length > 0 && data[0].key && data[0].value) {
    parsed = data.map(kv => {
      try{
        return JSON.parse(kv.value)
      }catch(_){
        // fallback to raw
        return { key: kv.key, value: kv.value }
      }
    })
  } else if (data && Array.isArray(data.nodes)) {
    // some backends may return { nodes: [...] }
    parsed = data.nodes
  } else {
    parsed = data
  }

  // Ensure we always return an array to avoid .map on non-array values in UI
  if (Array.isArray(parsed)) return parsed
  if (parsed == null) return []
  return [parsed]
}

export async function apiUnregisterNode(nodeUuid){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.delete(`/api/nodes/${nodeUuid}`, { headers })
  if(resp.status !== 200) throw new Error('failed to unregister node')
  return resp.data
}

// API for HSI configurations
export async function getHSIUserIds(nodeId){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.get(`/api/config/${nodeId}/hsi/users`, { headers })
  if(resp.status !== 200) throw new Error('failed to get HSI user IDs')
  return resp.data.user_ids || []
}

export async function getHSIConfig(nodeId, userId){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.get(`/api/config/${nodeId}/hsi/${userId}`, { headers })
  if(resp.status !== 200) throw new Error('failed to get HSI config')
  return resp.data
}

export async function createHSIConfig(nodeId, config){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.post(`/api/config/${nodeId}/hsi`, config, { headers })
  if(resp.status !== 200) throw new Error('failed to create HSI config')
  return resp.data
}

export async function updateHSIConfig(nodeId, userId, config){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.put(`/api/config/${nodeId}/hsi/${userId}`, config, { headers })
  if(resp.status !== 200) throw new Error('failed to update HSI config')
  return resp.data
}

export async function deleteHSIConfig(nodeId, userId){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.delete(`/api/config/${nodeId}/hsi/${userId}`, { headers })
  if(resp.status !== 200) throw new Error('failed to delete HSI config')
  return resp.data
}

export async function dialPPPoE(nodeId, userId){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.post(`/api/pppoe/dial`, { node_id: nodeId, user_id: userId }, { headers })
  if(resp.status !== 200) throw new Error('failed to dial PPPoE')
  return resp.data
}

export async function hangupPPPoE(nodeId, userId){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.post(`/api/pppoe/hangup`, { node_id: nodeId, user_id: userId }, { headers })
  if(resp.status !== 200) throw new Error('failed to hangup PPPoE')
  return resp.data
}

// Failed Events API
export async function getFailedEvents(nodeId){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.get(`/api/failed-events/${nodeId}`, { headers })
  if(resp.status !== 200) throw new Error('failed to get failed events')
  return resp.data.events || []
}

export async function getAllFailedEvents(eventType = null){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const params = eventType ? { event_type: eventType } : {}
  const resp = await axios.get(`/api/failed-events`, { headers, params })
  if(resp.status !== 200) throw new Error('failed to get all failed events')
  return resp.data.events || []
}

// Node Subscriber Count API
export async function getNodeSubscriberCount(nodeId){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.get(`/api/nodes/${nodeId}/subscriber-count`, { headers })
  if(resp.status !== 200) throw new Error('failed to get subscriber count')
  return resp.data
}

export async function updateNodeSubscriberCount(nodeId, subscriberCount){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.put(`/api/nodes/${nodeId}/subscriber-count`, 
    { subscriber_count: subscriberCount }, 
    { headers }
  )
  if(resp.status !== 200) throw new Error('failed to update subscriber count')
  return resp.data
}

// Static DNS Record API
export async function getDnsRecords(nodeId, userId){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.get(`/api/config/${nodeId}/dns/${userId}`, { headers })
  if(resp.status !== 200) throw new Error('failed to get DNS records')
  return resp.data.records || []
}

export async function getDnsRecord(nodeId, userId, domain){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.get(`/api/config/${nodeId}/dns/${userId}/${encodeURIComponent(domain)}`, { headers })
  if(resp.status !== 200) throw new Error('failed to get DNS record')
  return resp.data
}

export async function addOrUpdateDnsRecord(nodeId, userId, record){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.post(`/api/config/${nodeId}/dns/${userId}`, record, { headers })
  if(resp.status !== 200) throw new Error('failed to add/update DNS record')
  return resp.data
}

export async function deleteDnsRecord(nodeId, userId, domain){
  const token = localStorage.getItem('token')
  const headers = token ? { Authorization: token } : {}
  const resp = await axios.delete(`/api/config/${nodeId}/dns/${userId}/${encodeURIComponent(domain)}`, { headers })
  if(resp.status !== 200) throw new Error('failed to delete DNS record')
  return resp.data
}
