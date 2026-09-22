<script setup>
import { onMounted, ref } from 'vue'
import { api } from '../api.js'

const events = ref([])
const form = ref({ type: 'invoice.paid', payload: '{\n  "invoice_id": "inv_1001",\n  "amount": 9900\n}', orderKey: '' })
const result = ref('')
const error = ref('')

async function load() {
  events.value = await api.listEvents()
}
onMounted(load)

async function publish() {
  error.value = ''
  result.value = ''
  let payload
  try {
    payload = JSON.parse(form.value.payload || '{}')
  } catch (e) {
    error.value = 'payload 不是合法 JSON：' + e.message
    return
  }
  const body = { type: form.value.type, payload }
  if (form.value.orderKey.trim()) body.order_key = form.value.orderKey.trim()
  try {
    const r = await api.createEvent(body)
    result.value = `事件 #${r.event_id} 已创建，派生出 ${r.deliveries_created} 条投递`
    await load()
  } catch (e) {
    error.value = e.message
  }
}
</script>

<template>
  <section>
    <h2>事件入口</h2>
    <p class="muted">
      发送 <code>POST /api/events</code>：<code>{"type","payload","order_key?"}</code>。
      同一 <code>order_key</code> 的事件对同一回调严格按序投递。
    </p>
    <div class="grid2">
      <div>
        <label>事件类型
          <input v-model="form.type" />
        </label>
        <label>顺序键（可选）
          <input v-model="form.orderKey" placeholder="例如 customer-42" />
        </label>
        <label>Payload（JSON）
          <textarea v-model="form.payload" rows="10"></textarea>
        </label>
        <button class="primary" @click="publish">发布事件</button>
        <p v-if="result" class="ok-banner">{{ result }}</p>
        <p v-if="error" class="error-banner">{{ error }}</p>
      </div>
      <div>
        <h3>最近事件</h3>
        <table>
          <thead><tr><th>ID</th><th>类型</th><th>顺序键</th><th>Payload</th></tr></thead>
          <tbody>
            <tr v-for="ev in events" :key="ev.id">
              <td>{{ ev.id }}</td>
              <td class="mono">{{ ev.event_type }}</td>
              <td class="mono">{{ ev.order_key || '—' }}</td>
              <td class="mono break small">{{ JSON.stringify(ev.payload) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>
</template>
