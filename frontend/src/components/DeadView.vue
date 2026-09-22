<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api.js'
import { errorLabel, formatTime } from '../format.js'
import AttemptsDrawer from './AttemptsDrawer.vue'

const emit = defineEmits(['redriven'])
const dead = ref([])
const selected = ref(null)
const busyId = ref(null)
let timer

async function load() {
  dead.value = await api.listDeliveries('dead')
}
onMounted(() => {
  load()
  timer = setInterval(load, 3000)
})
onUnmounted(() => clearInterval(timer))

async function redrive(id) {
  busyId.value = id
  try {
    await api.redrive(id)
    emit('redriven')
    await load()
  } finally {
    busyId.value = null
  }
}
</script>

<template>
  <section>
    <h2>死信队列</h2>
    <p class="muted">
      达到最大尝试次数，或遇到 4xx（429 除外）永久失败的投递会被搁置在这里，不会自动再投。
    </p>
    <table v-if="dead.length">
      <thead>
        <tr><th>#</th><th>回调</th><th>事件</th><th>尝试</th><th>失败原因</th><th>最近更新</th><th></th></tr>
      </thead>
      <tbody>
        <tr v-for="d in dead" :key="d.id">
          <td>{{ d.id }}</td>
          <td class="mono">#{{ d.endpoint_id }}</td>
          <td class="mono">#{{ d.event_id }}</td>
          <td>{{ d.attempts }} / {{ d.max_attempts }}</td>
          <td>
            <strong class="error-kind" :title="d.error_detail">{{ d.error_kind ? errorLabel(d.error_kind) : '—' }}</strong>
            <div class="small muted break">{{ d.error_detail }}</div>
          </td>
          <td class="small">{{ formatTime(d.updated_at) }}</td>
          <td class="nowrap">
            <button class="primary" :disabled="busyId === d.id" @click="redrive(d.id)">重投</button>
            <button @click="selected = d">详情</button>
          </td>
        </tr>
      </tbody>
    </table>
    <p v-else class="muted">死信队列为空。</p>

    <AttemptsDrawer v-if="selected" :delivery="selected" @close="selected = null" @redriven="load" />
  </section>
</template>
