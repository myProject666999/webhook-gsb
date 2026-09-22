<script setup>
import { onMounted, ref } from 'vue'
import { api } from '../api.js'
import { attemptStatusLabel, errorLabel, formatTime, statusLabel } from '../format.js'

const props = defineProps({ delivery: Object })
const emit = defineEmits(['close', 'redriven'])
const attempts = ref([])
const latest = ref(props.delivery)
const busy = ref(false)
const note = ref('')

onMounted(async () => {
  attempts.value = await api.listAttempts(props.delivery.id)
  latest.value = await api.getDelivery(props.delivery.id)
})

async function redrive() {
  busy.value = true
  note.value = ''
  try {
    await api.redrive(props.delivery.id)
    note.value = '已重新入队，尝试次数已清零'
    latest.value = await api.getDelivery(props.delivery.id)
    attempts.value = await api.listAttempts(props.delivery.id)
    emit('redriven')
  } catch (e) {
    note.value = '重投失败：' + e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="drawer-mask" @click.self="emit('close')">
    <aside class="drawer">
      <div class="row-between">
        <h3>投递 #{{ delivery.id }}</h3>
        <button @click="emit('close')">×</button>
      </div>

      <dl class="detail-grid">
        <dt>回调</dt><dd class="mono">#{{ latest.endpoint_id }}</dd>
        <dt>事件</dt><dd class="mono">#{{ latest.event_id }}</dd>
        <dt>状态</dt><dd><span :class="['pill', latest.status]">{{ statusLabel(latest.status) }}</span></dd>
        <dt>尝试次数</dt><dd>{{ latest.attempts }} / {{ latest.max_attempts }}</dd>
        <dt>当前持有者</dt><dd class="mono">{{ latest.leased_by || '—' }}</dd>
        <dt>租约到期</dt><dd>{{ formatTime(latest.leased_until) }}</dd>
        <dt>下次可投</dt><dd>{{ formatTime(latest.not_before) }}</dd>
        <dt>最近错误</dt>
        <dd>
          <strong v-if="latest.error_kind">{{ errorLabel(latest.error_kind) }}</strong>
          <pre v-if="latest.error_detail" class="error-detail">{{ latest.error_detail }}</pre>
          <span v-if="!latest.error_kind">—</span>
        </dd>
      </dl>

      <h4>尝试历史</h4>
      <table class="attempts">
        <thead>
          <tr><th>#</th><th>结果</th><th>HTTP</th><th>耗时</th><th>执行者</th><th>时间</th><th>错误</th></tr>
        </thead>
        <tbody>
          <tr v-for="a in attempts" :key="a.id">
            <td>{{ a.attempt_no }}</td>
            <td><span :class="['pill', a.status === 'succeeded' ? 'succeeded' : a.status === 'lost_lease' ? 'dead' : 'pending']">{{ attemptStatusLabel(a.status) }}</span></td>
            <td>{{ a.http_status || '—' }}</td>
            <td>{{ a.duration_ms != null ? a.duration_ms + ' ms' : '—' }}</td>
            <td class="mono small">{{ a.worker_id || '—' }}</td>
            <td class="small">{{ formatTime(a.started_at) }}</td>
            <td>
              <span v-if="a.error_kind" class="error-kind" :title="a.error_detail">{{ errorLabel(a.error_kind) }}</span>
              <span v-else>—</span>
            </td>
          </tr>
        </tbody>
      </table>

      <div v-if="latest.status === 'dead'" class="redrive-box">
        <p>该投递已进死信队列，不会自动重试。可手动重新入队，尝试次数清零后从头再投。</p>
        <button class="primary" :disabled="busy" @click="redrive">手动重投</button>
        <span v-if="note" class="ok-banner">{{ note }}</span>
      </div>
    </aside>
  </div>
</template>
