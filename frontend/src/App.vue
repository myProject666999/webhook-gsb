<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { api } from './api.js'
import EndpointsView from './components/EndpointsView.vue'
import EventsView from './components/EventsView.vue'
import DeliveriesView from './components/DeliveriesView.vue'
import DeadView from './components/DeadView.vue'

const tab = ref('deliveries')
const stats = ref(null)
const error = ref('')
let timer

async function loadStats() {
  try {
    stats.value = await api.stats()
    error.value = ''
  } catch (e) {
    error.value = e.message
  }
}

onMounted(() => {
  loadStats()
  timer = setInterval(loadStats, 2000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <header class="topbar">
    <div class="brand">Webhook 投递服务</div>
    <nav>
      <button :class="{ active: tab === 'deliveries' }" @click="tab = 'deliveries'">投递历史</button>
      <button :class="{ active: tab === 'dead' }" @click="tab = 'dead'">
        死信队列<span v-if="stats?.dead" class="badge danger">{{ stats.dead }}</span>
      </button>
      <button :class="{ active: tab === 'endpoints' }" @click="tab = 'endpoints'">回调地址</button>
      <button :class="{ active: tab === 'events' }" @click="tab = 'events'">事件入口</button>
    </nav>
    <div class="stats" v-if="stats">
      <span>总计 {{ stats.total }}</span>
      <span class="dot pending"></span>{{ stats.pending }}
      <span class="dot inflight"></span>{{ stats.in_flight }}
      <span class="dot success"></span>{{ stats.succeeded }}
      <span class="dot dead"></span>{{ stats.dead }}
    </div>
  </header>

  <main class="content">
    <p v-if="error" class="error-banner">后端连接失败：{{ error }}</p>
    <DeliveriesView v-if="tab === 'deliveries'" />
    <DeadView v-else-if="tab === 'dead'" @redriven="loadStats" />
    <EndpointsView v-else-if="tab === 'endpoints'" />
    <EventsView v-else />
  </main>
</template>
