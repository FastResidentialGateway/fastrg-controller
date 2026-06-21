import React, { useEffect, useState } from 'react'
import { fetchNodes, apiClearInactiveNodes } from '../api'
import NodeCard from '../components/NodeCard'
import { useI18n } from '../i18n/I18nContext'
import useToast from '../components/ToastBridge'

export default function Nodes(){
  const [nodes, setNodes] = useState([])
  const [error, setError] = useState(null)
  const [loading, setLoading] = useState(false)
  const [clearing, setClearing] = useState(false)
  const { t } = useI18n()
  const { showToast } = useToast()

  const loadNodes = async () => {
    setLoading(true)
    setError(null)
    try{
      const data = await fetchNodes()
      setNodes(data)
    }catch(err){
      setError(err.message || t('nodes.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  useEffect(()=>{
    loadNodes()
  }, [])

  const handleNodeUnregistered = () => {
    // Reload node list
    loadNodes()
  }

  const inactiveCount = (Array.isArray(nodes) ? nodes : []).filter(n => n.status === 'inactive').length

  const handleClearInactive = async () => {
    if (inactiveCount === 0) return
    if (!window.confirm(t('nodes.confirmClearInactive').replace('{count}', inactiveCount))) {
      return
    }
    setClearing(true)
    try {
      const data = await apiClearInactiveNodes()
      const deleted = (data && typeof data.deleted === 'number') ? data.deleted : inactiveCount
      showToast(t('nodes.clearInactiveSuccess').replace('{count}', deleted), 3500, 'info')
      await loadNodes()
    } catch (err) {
      showToast(t('nodes.clearInactiveFailed') + ': ' + (err?.response?.data?.error || err.message || ''), 4500, 'error')
    } finally {
      setClearing(false)
    }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h2>{t('nodes.title')}</h2>
        {inactiveCount > 0 && (
          <button
            onClick={handleClearInactive}
            disabled={clearing}
            style={{
              backgroundColor: '#dc3545',
              color: 'white',
              border: 'none',
              borderRadius: '4px',
              padding: '8px 14px',
              cursor: clearing ? 'not-allowed' : 'pointer',
              fontSize: '13px',
              opacity: clearing ? 0.6 : 1
            }}
          >
            {t('nodes.clearInactive').replace('{count}', inactiveCount)}
          </button>
        )}
      </div>
      {error && <div className="error">{error}</div>}
      {loading && <div>{t('nodes.loading')}</div>}
      <div className="nodes-grid">
        {(Array.isArray(nodes) ? nodes : []).map(n => (
          <NodeCard
            key={n.node_uuid || n.uuid || n.node_id || n.id || n.key}
            node={n}
            onNodeUnregistered={handleNodeUnregistered}
          />
        ))}
      </div>
    </div>
  )
}
