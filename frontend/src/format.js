export function statusLabel(s) {
  return {
    pending: '等待中',
    in_flight: '投递中',
    succeeded: '已成功',
    dead: '死信'
  }[s] || s
}

export function attemptStatusLabel(s) {
  return {
    succeeded: '成功',
    timeout: '超时',
    http_error: 'HTTP 错误',
    network_error: '网络错误',
    rejected: '永久拒绝',
    lost_lease: '租约已被接管'
  }[s] || s
}

export function errorLabel(k) {
  return {
    timeout: '超时',
    http_5xx: '服务端 5xx',
    http_4xx: '客户端 4xx（不重试）',
    network: '网络错误',
    lease_timeout: '租约超时被接管',
    lease_lost: '租约丢失',
    bad_response: '响应异常'
  }[k] || k
}

export function formatTime(v) {
  if (!v) return '—'
  const d = new Date(v)
  const pad = (n) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}
