async function request(path, options = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...options
  })
  const text = await res.text()
  const data = text ? JSON.parse(text) : null
  if (!res.ok) {
    throw new Error(data?.error || `HTTP ${res.status}`)
  }
  return data
}

export const api = {
  stats: () => request('/api/stats'),
  listEndpoints: () => request('/api/endpoints'),
  createEndpoint: (body) => request('/api/endpoints', { method: 'POST', body: JSON.stringify(body) }),
  updateEndpoint: (id, body) => request(`/api/endpoints/${id}`, { method: 'PUT', body: JSON.stringify(body) }),
  deleteEndpoint: (id) => request(`/api/endpoints/${id}`, { method: 'DELETE' }),
  listEvents: () => request('/api/events'),
  createEvent: (body) => request('/api/events', { method: 'POST', body: JSON.stringify(body) }),
  listDeliveries: (status = '', endpointId = '') => {
    const qs = new URLSearchParams()
    if (status) qs.set('status', status)
    if (endpointId) qs.set('endpoint_id', endpointId)
    const suffix = qs.toString() ? `?${qs}` : ''
    return request(`/api/deliveries${suffix}`)
  },
  getDelivery: (id) => request(`/api/deliveries/${id}`),
  listAttempts: (id) => request(`/api/deliveries/${id}/attempts`),
  redrive: (id) => request(`/api/deliveries/${id}/redrive`, { method: 'POST' })
}
