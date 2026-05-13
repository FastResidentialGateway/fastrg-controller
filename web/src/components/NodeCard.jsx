import React, { useState } from 'react'
import { apiUnregisterNode, getNodeSubscriberCount, updateNodeSubscriberCount } from '../api'
import { useNavigate } from 'react-router-dom'
import { useI18n } from '../i18n/I18nContext'
import useToast from './ToastBridge'

export default function NodeCard({node, onNodeUnregistered}){
  const navigate = useNavigate()
  const { t } = useI18n()
  const { showToast, hideToast } = useToast()
  const [showSubscriberModal, setShowSubscriberModal] = useState(false)
  const [subscriberCount, setSubscriberCount] = useState('')
  const [currentSubscriberCount, setCurrentSubscriberCount] = useState('')
  const [loadingSubscriberCount, setLoadingSubscriberCount] = useState(false)

  // Parse node data
  let nodeData = node;
  if (typeof node.value === 'string') {
    try {
      nodeData = JSON.parse(node.value);
    } catch (e) {
      // parse error suppressed in production UI
      nodeData = { node_uuid: 'parse error' };
    }
  }

  const handleUnregister = async () => {
    const nodeUuid = nodeData.uuid || nodeData.node_uuid;
    if (!nodeUuid) {
      alert(t('nodes.cannotGetUuid'));
      return;
    }

    if (!window.confirm(t('nodes.confirmUnregister').replace('{uuid}', nodeUuid))) {
      return;
    }

    try {
      await apiUnregisterNode(nodeUuid);
      const { showToast } = useToast()
      showToast(t('nodes.unregisterSuccess'), 3500, 'info')
      // Notify parent component to reload node list
      if (onNodeUnregistered) {
        onNodeUnregistered();
      }
    } catch (error) {
      // On unregister failure: show an error to the user but do not print detailed info to the console
      showToast(t('nodes.unregisterFailed') + ': ' + (error?.response?.data?.error || error.message || ''), 4500, 'error')
    }
  };

  const handleConfigHSI = () => {
    const nodeUuid = nodeData.uuid || nodeData.node_uuid;
    if (!nodeUuid) {
      alert(t('nodes.cannotGetUuid'));
      return;
    }
    navigate(`/nodes/${nodeUuid}/hsi`);
  };

  const handleOpenSubscriberModal = async () => {
    const nodeUuid = nodeData.uuid || nodeData.node_uuid;
    if (!nodeUuid) {
      alert(t('nodes.cannotGetUuid'));
      return;
    }

    setLoadingSubscriberCount(true);
    setShowSubscriberModal(true);

    try {
      const data = await getNodeSubscriberCount(nodeUuid);
      const count = data.subscriber_count.toString();
      setCurrentSubscriberCount(count);
      setSubscriberCount(count);
    } catch (error) {
      // If not found, default to 0
      if (error.response && error.response.status === 404) {
        setCurrentSubscriberCount('0');
        setSubscriberCount('0');
      } else {
        // Failed to fetch subscriber count: show a user-facing error and suppress console output
        showToast(t('nodes.getSubscriberCountFailed') + ': ' + (error?.response?.data?.error || error.message || ''), 4500, 'error')
        setCurrentSubscriberCount('0');
        setSubscriberCount('0');
      }
    } finally {
      setLoadingSubscriberCount(false);
    }
  };

  const handleUpdateSubscriberCount = async () => {
    const nodeUuid = nodeData.uuid || nodeData.node_uuid;
    if (!nodeUuid) {
      alert(t('nodes.cannotGetUuid'));
      return;
    }

    const count = parseInt(subscriberCount, 10);
    if (isNaN(count) || count < 0) {
      alert(t('nodes.invalidSubscriberCount'));
      return;
    }

    try {
      await updateNodeSubscriberCount(nodeUuid, count);
      showToast(t('nodes.updateSubscriberCountSuccess'), 3500, 'info')
      setShowSubscriberModal(false);
    } catch (error) {
      // Failed to update subscriber count: show a user-facing error and suppress console output
      showToast(t('nodes.updateSubscriberCountFailed') + ': ' + (error?.response?.data?.error || error.message || ''), 4500, 'error')
    }
  };

  const handleCloseSubscriberModal = () => {
    setShowSubscriberModal(false);
    setSubscriberCount('');
    setCurrentSubscriberCount('');
  };

  return (
    <>
      <div className="node-card">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h3>{nodeData.uuid || nodeData.node_uuid || node.key || 'unknown'}</h3>
        <button 
          onClick={handleUnregister}
          style={{
            backgroundColor: '#dc3545',
            color: 'white',
            border: 'none',
            borderRadius: '4px',
            padding: '5px 10px',
            cursor: 'pointer',
            fontSize: '12px'
          }}
        >
          {t('nodes.unregister')}
        </button>
      </div>

      {/* Subscriber/config buttons area */}
      <div style={{ marginTop: '10px', display: 'flex', gap: '10px' }}>
        <button 
          onClick={handleConfigHSI}
          style={{
            backgroundColor: '#007bff',
            color: 'white',
            border: 'none',
            borderRadius: '4px',
            padding: '8px 12px',
            cursor: 'pointer',
            fontSize: '12px'
          }}
        >
          {t('nodes.configHSI')}
        </button>
        <button 
          onClick={handleOpenSubscriberModal}
          style={{
            backgroundColor: '#28a745',
            color: 'white',
            border: 'none',
            borderRadius: '4px',
            padding: '8px 12px',
            cursor: 'pointer',
            fontSize: '12px'
          }}
        >
          {t('nodes.setSubscriberCount')}
        </button>
      </div>

      <ul>
        {nodeData.node_ip && <li><strong>{t('nodes.nodeIp')}:</strong> {nodeData.node_ip}</li>}
        {nodeData.ip && <li><strong>{t('nodes.ip')}:</strong> {nodeData.ip}</li>}
        {nodeData.version && <li><strong>{t('nodes.version')}:</strong> {nodeData.version}</li>}
        {nodeData.host_os && <li><strong>{t('nodes.hostOs')}:</strong> {nodeData.host_os}</li>}
        {nodeData.uptime != null && nodeData.last_seen_time && (() => {
          const bootTime = new Date((Number(nodeData.last_seen_time) - Number(nodeData.uptime)) * 1000)
          const pad = n => String(n).padStart(2, '0')
          const formatted = `${bootTime.getFullYear()}/${pad(bootTime.getMonth()+1)}/${pad(bootTime.getDate())} ${pad(bootTime.getHours())}:${pad(bootTime.getMinutes())}:${pad(bootTime.getSeconds())}`
          return <li><strong>{t('nodes.hostUptime')}:</strong> Since {formatted}</li>
        })()}
        {nodeData.last_seen_time && (
          <li><strong>{t('nodes.lastSeen')}:</strong> {new Date(Number(nodeData.last_seen_time) * 1000).toLocaleString()}</li>
        )}
        {nodeData.registered_at && (
          <li><strong>{t('nodes.registered')}:</strong> {new Date(Number(nodeData.registered_at) * 1000).toLocaleString()}</li>
        )}
        {nodeData.status && <li><strong>{t('nodes.status')}:</strong> {nodeData.status}</li>}
      </ul>
    </div>

    {/* Subscriber Count Modal */}
    {showSubscriberModal && (
      <div style={{
        position: 'fixed',
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        backgroundColor: 'rgba(0,0,0,0.5)',
        display: 'flex',
        justifyContent: 'center',
        alignItems: 'center',
        zIndex: 1000
      }}>
        <div style={{
          backgroundColor: 'white',
          padding: '20px',
          borderRadius: '8px',
          minWidth: '300px',
          maxWidth: '500px'
        }}>
          <h3>{t('nodes.setSubscriberCount')}</h3>
          
          {/* Display current subscriber count */}
          {loadingSubscriberCount ? (
            <div style={{ 
              marginTop: '15px', 
              padding: '10px', 
              backgroundColor: '#f8f9fa', 
              borderRadius: '4px',
              textAlign: 'center'
            }}>
              {t('common.loading')}
            </div>
          ) : (
            <div style={{ 
              marginTop: '15px', 
              padding: '10px', 
              backgroundColor: '#e7f3ff', 
              borderRadius: '4px',
              border: '1px solid #b3d9ff'
            }}>
              <strong>{t('nodes.currentSubscriberCount')}:</strong> {currentSubscriberCount || '0'}
            </div>
          )}

          <div style={{ marginTop: '15px' }}>
            <label style={{ display: 'block', marginBottom: '5px' }}>
              {t('nodes.newSubscriberCountLabel')}:
            </label>
            <input
              type="number"
              min="0"
              value={subscriberCount}
              onChange={(e) => setSubscriberCount(e.target.value)}
              style={{
                width: '100%',
                padding: '8px',
                border: '1px solid #ccc',
                borderRadius: '4px',
                fontSize: '14px'
              }}
              disabled={loadingSubscriberCount}
            />
          </div>
          <div style={{ marginTop: '20px', display: 'flex', gap: '10px', justifyContent: 'flex-end' }}>
            <button
              onClick={handleCloseSubscriberModal}
              style={{
                padding: '8px 16px',
                backgroundColor: '#6c757d',
                color: 'white',
                border: 'none',
                borderRadius: '4px',
                cursor: 'pointer'
              }}
            >
              {t('common.cancel')}
            </button>
            <button
              onClick={handleUpdateSubscriberCount}
              disabled={loadingSubscriberCount}
              style={{
                padding: '8px 16px',
                backgroundColor: '#28a745',
                color: 'white',
                border: 'none',
                borderRadius: '4px',
                cursor: loadingSubscriberCount ? 'not-allowed' : 'pointer',
                opacity: loadingSubscriberCount ? 0.6 : 1
              }}
            >
              {t('common.save')}
            </button>
          </div>
        </div>
      </div>
    )}
    </>
  )
}
