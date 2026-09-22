<script setup>
import { onMounted, ref } from 'vue'
import { api } from '../api.js'

const endpoints = ref([])
const editing = ref(null)
const error = ref('')

const empty = () => ({ url: '', secret: '', eventsText: '', active: true, description: '' })

async function load() {
  endpoints.value = await api.listEndpoints()
}
onMounted(load)

function startCreate() {
  editing.value = { ...empty() }
}
function startEdit(ep) {
  editing.value = {
    id: ep.id,
    url: ep.url,
    secret: ep.secret,
    eventsText: ep.events.join(', '),
    active: ep.active,
    description: ep.description
  }
}
async function save() {
  error.value = ''
  const body = {
    url: editing.value.url,
    secret: editing.value.secret,
    events: editing.value.eventsText.split(',').map(s => s.trim()).filter(Boolean),
    active: editing.value.active,
    description: editing.value.description
  }
  try {
    if (editing.value.id) {
      await api.updateEndpoint(editing.value.id, body)
    } else {
      await api.createEndpoint(body)
    }
    editing.value = null
    await load()
  } catch (e) {
    error.value = e.message
  }
}
async function remove(ep) {
  if (!confirm(`删除回调 ${ep.url}？其投递历史会一并删除。`)) return
  await api.deleteEndpoint(ep.id)
  await load()
}
</script>

<template>
  <section>
    <div class="row-between">
      <h2>回调地址</h2>
      <button class="primary" @click="startCreate">+ 登记回调</button>
    </div>
    <p v-if="error" class="error-banner">{{ error }}</p>

    <table v-if="endpoints.length">
      <thead>
        <tr>
          <th>ID</th><th>URL</th><th>订阅事件</th><th>密钥</th><th>状态</th><th>说明</th><th></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="ep in endpoints" :key="ep.id">
          <td>{{ ep.id }}</td>
          <td class="mono break">{{ ep.url }}</td>
          <td>
            <span v-for="ev in ep.events" :key="ev" class="chip">{{ ev }}</span>
          </td>
          <td class="mono">{{ ep.secret.slice(0, 3) }}••••••</td>
          <td>
            <span :class="['pill', ep.active ? 'ok' : 'off']">{{ ep.active ? '启用' : '停用' }}</span>
          </td>
          <td>{{ ep.description }}</td>
          <td class="nowrap">
            <button @click="startEdit(ep)">编辑</button>
            <button class="danger-text" @click="remove(ep)">删除</button>
          </td>
        </tr>
      </tbody>
    </table>
    <p v-else class="muted">还没有回调地址，点「登记回调」开始。</p>

    <div v-if="editing" class="modal" @click.self="editing = null">
      <div class="modal-card">
        <h3>{{ editing.id ? '编辑回调' : '登记回调' }}</h3>
        <label>回调 URL
          <input v-model="editing.url" placeholder="https://example.com/webhooks" />
        </label>
        <label>签名密钥（HMAC，至少 8 位）
          <input v-model="editing.secret" placeholder="long-random-secret" />
        </label>
        <label>订阅事件类型（逗号分隔）
          <input v-model="editing.eventsText" placeholder="invoice.paid, order.created" />
        </label>
        <label>说明
          <input v-model="editing.description" placeholder="可选" />
        </label>
        <label class="checkbox">
          <input type="checkbox" v-model="editing.active" /> 启用
        </label>
        <div class="row-end gap">
          <button @click="editing = null">取消</button>
          <button class="primary" @click="save">保存</button>
        </div>
      </div>
    </div>
  </section>
</template>
