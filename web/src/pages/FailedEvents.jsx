import React, { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { getAllFailedEvents, deleteFailedEvents } from '../api'
import { useI18n } from '../i18n/I18nContext'

export default function FailedEvents() {
  const navigate = useNavigate()
  const { t } = useI18n()
  const [events, setEvents] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [eventTypeFilter, setEventTypeFilter] = useState('')
  const [selectedIds, setSelectedIds] = useState(new Set())
  const [deleting, setDeleting] = useState(false)

  useEffect(() => {
    fetchEvents()

    let interval
    if (autoRefresh) {
      interval = setInterval(fetchEvents, 10000)
    }

    return () => {
      if (interval) clearInterval(interval)
    }
  }, [autoRefresh, eventTypeFilter])

  const fetchEvents = async () => {
    try {
      const data = await getAllFailedEvents(eventTypeFilter || null)
      setEvents(data)
      setError(null)
      // Drop selections for events that no longer exist.
      setSelectedIds(prev => {
        const existing = new Set(data.map(e => e.id))
        return new Set([...prev].filter(id => existing.has(id)))
      })
    } catch (err) {
      setError(err.message || 'Failed to fetch events')
    } finally {
      setLoading(false)
    }
  }

  const formatTimestamp = (eventTime) => {
    if (!eventTime) return ''
    const date = new Date(eventTime)
    return isNaN(date.getTime()) ? eventTime : date.toLocaleString()
  }

  const getEventTypeColor = (eventType) => {
    const colors = {
      'CONFIG_APPLY_OK': '#28a745',
      'CONFIG_APPLY_FAIL': '#dc3545',
      'RUNTIME_ERROR': '#dc3545',
      'default': '#6c757d'
    }
    return colors[eventType] || colors.default
  }

  const allIds = events.map(e => e.id)
  const allSelected = allIds.length > 0 && allIds.every(id => selectedIds.has(id))
  const someSelected = allIds.some(id => selectedIds.has(id))

  const toggleSelectAll = () => {
    if (allSelected) {
      setSelectedIds(new Set())
    } else {
      setSelectedIds(new Set(allIds))
    }
  }

  const toggleSelect = (id) => {
    setSelectedIds(prev => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }

  const handleDeleteSelected = async () => {
    const ids = [...selectedIds]
    if (ids.length === 0) return

    const confirmMsg = t('events.confirmDelete').replace('{count}', ids.length)
    if (!window.confirm(confirmMsg)) return

    setDeleting(true)
    try {
      const result = await deleteFailedEvents(ids)
      const deleted = result.deleted ?? ids.length
      alert(t('events.deleteSuccess').replace('{count}', deleted))
      setSelectedIds(new Set())
      await fetchEvents()
    } catch (err) {
      alert(t('events.deleteFailed') + ': ' + (err.message || ''))
    } finally {
      setDeleting(false)
    }
  }

  return (
    <div style={{ padding: '20px' }}>
      <div style={{ marginBottom: '20px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
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
            {t('hsi.back')}
          </button>
          <h2>{t('events.title')}</h2>
        </div>
        <div style={{ display: 'flex', gap: '10px', alignItems: 'center' }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: '5px' }}>
            {t('events.filterByType')}:
            <select
              value={eventTypeFilter}
              onChange={(e) => setEventTypeFilter(e.target.value)}
              style={{
                padding: '6px 10px',
                border: '1px solid #ccc',
                borderRadius: '4px',
                backgroundColor: 'white'
              }}
            >
              <option value="">{t('events.allTypes')}</option>
              <option value="CONFIG_APPLY_FAIL">Config Apply Fail</option>
              <option value="CONFIG_APPLY_OK">Config Apply OK</option>
              <option value="RUNTIME_ERROR">Runtime Error</option>
            </select>
          </label>
          <label style={{ display: 'flex', alignItems: 'center', gap: '5px' }}>
            <input
              type="checkbox"
              checked={autoRefresh}
              onChange={(e) => setAutoRefresh(e.target.checked)}
            />
            {t('common.refresh')} (10s)
          </label>
          <button
            onClick={fetchEvents}
            disabled={loading}
            style={{
              backgroundColor: '#007bff',
              color: 'white',
              border: 'none',
              borderRadius: '4px',
              padding: '8px 16px',
              cursor: loading ? 'not-allowed' : 'pointer'
            }}
          >
            {loading ? t('common.loading') : '🔄 ' + t('common.refresh')}
          </button>
          {someSelected && (
            <button
              onClick={handleDeleteSelected}
              disabled={deleting}
              style={{
                backgroundColor: '#dc3545',
                color: 'white',
                border: 'none',
                borderRadius: '4px',
                padding: '8px 16px',
                cursor: deleting ? 'not-allowed' : 'pointer'
              }}
            >
              {deleting ? t('common.processing') : `🗑 ${t('events.deleteSelected')} (${selectedIds.size})`}
            </button>
          )}
        </div>
      </div>

      {error && (
        <div style={{
          backgroundColor: '#f8d7da',
          color: '#721c24',
          padding: '10px',
          borderRadius: '4px',
          marginBottom: '20px'
        }}>
          {t('common.error')}: {error}
        </div>
      )}

      {loading && events.length === 0 ? (
        <div style={{ textAlign: 'center', padding: '20px' }}>
          {t('common.loading')}
        </div>
      ) : events.length === 0 ? (
        <div style={{
          backgroundColor: '#d1ecf1',
          color: '#0c5460',
          padding: '15px',
          borderRadius: '4px'
        }}>
          {t('events.noEvents')}
        </div>
      ) : (
        <div>
          <div style={{ marginBottom: '10px', color: '#666' }}>
            {events.length} {t('events.noEvents')}
          </div>
          <div style={{ overflowX: 'auto' }}>
            <table style={{
              width: '100%',
              borderCollapse: 'collapse',
              backgroundColor: 'white',
              boxShadow: '0 2px 4px rgba(0,0,0,0.1)'
            }}>
              <thead>
                <tr style={{ backgroundColor: '#f8f9fa' }}>
                  <th style={{ ...tableHeaderStyle, width: '40px', textAlign: 'center' }}>
                    <input
                      type="checkbox"
                      checked={allSelected}
                      ref={el => { if (el) el.indeterminate = someSelected && !allSelected }}
                      onChange={toggleSelectAll}
                      title={t('events.selectAll')}
                    />
                  </th>
                  <th style={tableHeaderStyle}>Time</th>
                  <th style={tableHeaderStyle}>Event type</th>
                  <th style={tableHeaderStyle}>Node ID</th>
                  <th style={tableHeaderStyle}>User ID</th>
                  <th style={tableHeaderStyle}>Module / Action</th>
                  <th style={tableHeaderStyle}>Error Code</th>
                  <th style={tableHeaderStyle}>Error Message</th>
                </tr>
              </thead>
              <tbody>
                {events.map((event, index) => {
                  const isSelected = selectedIds.has(event.id)
                  return (
                    <tr
                      key={event.id ?? index}
                      style={{
                        borderBottom: '1px solid #dee2e6',
                        backgroundColor: isSelected
                          ? '#fff3cd'
                          : index % 2 === 0 ? 'white' : '#f8f9fa'
                      }}
                    >
                      <td style={{ ...tableCellStyle, textAlign: 'center' }}>
                        <input
                          type="checkbox"
                          checked={isSelected}
                          onChange={() => toggleSelect(event.id)}
                        />
                      </td>
                      <td style={tableCellStyle}>
                        {formatTimestamp(event.event_time)}
                      </td>
                      <td style={tableCellStyle}>
                        <span style={{
                          backgroundColor: getEventTypeColor(event.event_type),
                          color: 'white',
                          padding: '4px 8px',
                          borderRadius: '4px',
                          fontSize: '12px',
                          fontWeight: 'bold'
                        }}>
                          {event.event_type}
                        </span>
                      </td>
                      <td style={tableCellStyle}>
                        <code style={{
                          backgroundColor: '#f1f1f1',
                          padding: '2px 6px',
                          borderRadius: '3px'
                        }}>
                          {event.node_uuid}
                        </code>
                      </td>
                      <td style={tableCellStyle}>
                        <code style={{
                          backgroundColor: '#f1f1f1',
                          padding: '2px 6px',
                          borderRadius: '3px'
                        }}>
                          {event.user_id}
                        </code>
                      </td>
                      <td style={tableCellStyle}>
                        {event.module || event.action || '-'}
                      </td>
                      <td style={tableCellStyle}>
                        {event.error_code ? (
                          <span style={{
                            backgroundColor: '#dc3545',
                            color: 'white',
                            padding: '4px 8px',
                            borderRadius: '4px',
                            fontSize: '12px',
                            fontWeight: 'bold'
                          }}>
                            {event.error_code}
                          </span>
                        ) : '-'}
                      </td>
                      <td style={{ ...tableCellStyle, maxWidth: '320px' }}>
                        {event.error_message || '-'}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  )
}

const tableHeaderStyle = {
  padding: '12px',
  textAlign: 'left',
  borderBottom: '2px solid #dee2e6',
  fontWeight: 'bold',
  color: '#495057'
}

const tableCellStyle = {
  padding: '12px',
  textAlign: 'left'
}
