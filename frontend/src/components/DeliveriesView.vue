<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api.js'
import { errorLabel, statusLabel, formatTime } from '../format.js'
import AttemptsDrawer from './AttemptsDrawer.vue'

const deliveries = ref([])
const endpoints = ref([])
const filterStatus = ref('')
const filterEndpoint = ref('')
const selected = ref(null)
let timer

async function load() {
  try {
    deliveries.value = await api.listDeliveries(filterStatus.value, filterEndpoint.value)
  } catch (e) {
    // keep last good list; banner is global
  }
}
onMounted(async () => {
  endpoints.value = await api.listEndpoints()
  load()
  timer = setInterval(load, 2000)
})
onUnmounted(() => clearInterval(timer))

function open(d) {
  selected.value = d
}
function closed() {
  selected.value = null
  load()
}
</script>

<template>
  <section>
    <div class="row-between">
      <h2>投递历史</h2>
      <div class="filters">
        <select v-model="filterStatus" @change="load">
          <option value="">全部状态</option>
          <option value="pending">等待中</option>
          <option value="in_flight">投递中</option>
          <option value="succeeded">已成功</option>
          <option value="dead">死信</option>
        </select>
        <select v-model="filterEndpoint" @change="load">
          <option value="">全部回调</option>
          <option v-for="ep in endpoints" :key="ep.id" :value="ep.id">#{{ ep.id }} {{ ep.url }}</option>
        </select>
        <button @click="load">刷新</button>
      </div>
    </div>

    <table>
      <thead>
        <tr>
          <th>#</th><th>回调</th><th>事件</th><th>状态</th><th>尝试</th>
          <th>队列位置</th><th>持有者</th><th>下次/最近</th><th>失败原因</th><th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="d in deliveries" :key="d.id">
          <td>{{ d.id }}</td>
          <td class="mono">#{{ d.endpoint_id }}</td>
          <td class="mono">#{{ d.event_id }}</td>
          <td><span :class="['pill', d.status]">{{ statusLabel(d.status) }}</span></td>
          <td>{{ d.attempts }} / {{ d.max_attempts }}</td>
          <td>
            <span v-if="d.queue_position > 1" class="queue-pos">
              第 {{ d.queue_position }} 位 · 队头阻塞
            </span>
            <span v-else>—</span>
          </td>
          <td class="mono small">{{ d.leased_by || '—' }}</td>
          <td class="small">{{ formatTime(d.updated_at) }}</td>
          <td>
            <span v-if="d.error_kind" class="error-kind" :title="d.error_detail">
              {{ errorLabel(d.error_kind) }}
            </span>
            <span v-else>—</span>
          </td>
          <td><button @click="open(d)">详情</button></td>
        </tr>
      </tbody>
    </table>

    <AttemptsDrawer v-if="selected" :delivery="selected" @close="closed" />
  </section>
</template>
