const state = {
  token: localStorage.getItem('monitor_token') || '',
  config: null,
  pollTimer: null
}

const $ = (selector) => document.querySelector(selector)
const $$ = (selector) => Array.from(document.querySelectorAll(selector))

function api(path, options = {}) {
  const headers = {
    ...(options.headers || {}),
    Authorization: `Bearer ${state.token}`
  }
  if (options.body && !headers['Content-Type']) {
    headers['Content-Type'] = 'application/json'
  }
  return fetch(path, { ...options, headers }).then(async (res) => {
    const text = await res.text()
    const data = text ? JSON.parse(text) : null
    if (!res.ok) {
      throw new Error(data?.error || data?.message || `HTTP ${res.status}`)
    }
    return data
  })
}

function toast(message, bad = false) {
  const node = $('#toast')
  node.textContent = message
  node.classList.toggle('bad', bad)
  node.classList.remove('hidden')
  clearTimeout(node._timer)
  node._timer = setTimeout(() => node.classList.add('hidden'), 3200)
}

function setConnected(connected) {
  $('#connectionState').textContent = connected ? '已连接' : '未连接'
  $('#connectionState').className = `status-pill ${connected ? 'good' : 'muted'}`
  $('#loginPanel').classList.toggle('hidden', connected)
  $('#workspace').classList.toggle('hidden', !connected)
  $('#logoutBtn').classList.toggle('hidden', !connected)
}

function stateClass(value) {
  if (value === 'healthy') return 'healthy'
  if (value === 'unhealthy') return 'unhealthy'
  return 'unknown'
}

function fmtTime(value) {
  if (!value || value.startsWith('0001-')) return '-'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '-'
  return d.toLocaleString()
}

function escapeHtml(value) {
  return String(value ?? '').replace(/[&<>"']/g, (ch) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  })[ch])
}

function renderAccounts(payload) {
  const items = payload.items || []
  const counts = {
    total: items.length,
    healthy: 0,
    unhealthy: 0,
    unknown: 0
  }
  for (const item of items) {
    const s = item.last_status?.state || 'unknown'
    if (s === 'healthy') counts.healthy += 1
    else if (s === 'unhealthy') counts.unhealthy += 1
    else counts.unknown += 1
  }
  $('#metricTotal').textContent = counts.total
  $('#metricHealthy').textContent = counts.healthy
  $('#metricUnhealthy').textContent = counts.unhealthy
  $('#metricUnknown').textContent = counts.unknown

  $('#emptyAccounts').classList.toggle('hidden', items.length > 0)
  $('#accountsBody').innerHTML = items.map((item) => {
    const status = item.last_status || {}
    const cls = stateClass(status.state)
    const name = item.name || status.account_name || '-'
    const detail = status.error_message || status.detail || ''
    return `
      <tr>
        <td>${item.id}</td>
        <td>
          <strong>${escapeHtml(name)}</strong>
          ${item.model_id ? `<div class="muted-text">测试模型 ${escapeHtml(item.model_id)}</div>` : ''}
        </td>
        <td>${escapeHtml(item.platform || '-')}<div class="muted-text">${escapeHtml(item.type || '')}</div></td>
        <td>
          <label class="switch-row" title="${item.enabled ? '点击后暂停后台监控' : '点击后启用后台监控'}">
            <input type="checkbox" data-action="enabled" data-id="${item.id}" ${item.enabled ? 'checked' : ''} />
            <span>${item.enabled ? '启用' : '暂停'}</span>
          </label>
        </td>
        <td>
          <span class="state ${cls}">${escapeHtml(status.state || 'unknown')}</span>
          <div class="muted-text">${escapeHtml(status.upstream_status || '-')}</div>
          ${detail ? `<div class="muted-text">${escapeHtml(detail)}</div>` : ''}
        </td>
        <td>${item.consecutive_failures}</td>
        <td>${fmtTime(status.checked_at)}</td>
        <td>${status.latency_ms || 0}ms</td>
        <td class="actions">
          <div class="row-actions">
            <button type="button" data-action="check" data-id="${item.id}">检查</button>
            <button type="button" class="ghost" data-action="edit" data-id="${item.id}" data-name="${escapeHtml(name)}" data-model="${escapeHtml(item.model_id || '')}">编辑</button>
            <button type="button" class="danger" data-action="delete" data-id="${item.id}">移除</button>
          </div>
        </td>
      </tr>`
  }).join('')
}

async function loadAccounts(silent = false) {
  try {
    const data = await api('/api/monitor-accounts')
    renderAccounts(data)
    setConnected(true)
  } catch (err) {
    setConnected(false)
    if (!silent) toast(err.message, true)
    stopPolling()
  }
}

async function loadConfig() {
  const cfg = await api('/api/config')
  state.config = cfg
  fillConfigForm(cfg)
}

function startPolling() {
  stopPolling()
  state.pollTimer = setInterval(() => loadAccounts(true), 10000)
}

function stopPolling() {
  if (state.pollTimer) clearInterval(state.pollTimer)
  state.pollTimer = null
}

function switchView(name) {
  $$('.tab').forEach((tab) => tab.classList.toggle('active', tab.dataset.view === name))
  $$('.view').forEach((view) => view.classList.remove('active'))
  $(`#${name}View`).classList.add('active')
}

function parseJSONField(value, fallback) {
  const trimmed = value.trim()
  if (!trimmed) return fallback
  return JSON.parse(trimmed)
}

function defaultBaseURL(platform) {
  if (platform === 'openai') return 'https://api.openai.com'
  if (platform === 'gemini') return 'https://generativelanguage.googleapis.com'
  if (platform === 'anthropic') return 'https://api.anthropic.com'
  return ''
}

function createCredentialsPlaceholder(platform, type) {
  if (type === 'apikey') {
    const example = { api_key: "sk-..." }
    const baseURL = defaultBaseURL(platform)
    if (baseURL) example.base_url = baseURL
    return JSON.stringify(example, null, 2)
  }
  if (type === 'oauth') {
    return JSON.stringify({ refresh_token: "..." }, null, 2)
  }
  if (type === 'setup-token') {
    return JSON.stringify({ refresh_token: "..." }, null, 2)
  }
  if (type === 'service_account') {
    return JSON.stringify({ service_account_json: {} }, null, 2)
  }
  return "{}"
}

function updateCreateAccountFields() {
  const form = $('#createAccountForm')
  const type = form.elements['type'].value
  const platform = form.elements['platform'].value
  form.elements['credentials'].placeholder = createCredentialsPlaceholder(platform, type)
}

function parseCSVNumbers(value) {
  return value.split(/[,\s;]+/).map((v) => Number(v.trim())).filter((v) => Number.isFinite(v) && v > 0)
}

function parseRecipients(value) {
  const seen = new Set()
  return value
    .split(/[,\s;]+/)
    .map((v) => v.trim())
    .filter(Boolean)
    .filter((v) => {
      const key = v.toLowerCase()
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
}

function optionalNumber(value) {
  if (String(value || '').trim() === '') return undefined
  const n = Number(value)
  return Number.isFinite(n) ? n : undefined
}

function datetimeLocalToUnix(value) {
  if (!value) return undefined
  const ts = new Date(value).getTime()
  if (!Number.isFinite(ts)) return undefined
  return Math.floor(ts / 1000)
}

function fillConfigForm(cfg) {
  const form = $('#settingsForm')
  form.elements['listen'].value = cfg.listen || ''
  form.elements['auth_token'].value = cfg.auth_token || ''
  form.elements['api_key'].value = cfg.api_key || ''
  form.elements['sub2api.base_url'].value = cfg.sub2api?.base_url || ''
  form.elements['sub2api.admin_api_key'].value = cfg.sub2api?.admin_api_key || ''
  form.elements['monitor.enabled'].checked = Boolean(cfg.monitor?.enabled)
  form.elements['monitor.check_interval_seconds'].value = cfg.monitor?.check_interval_seconds || 5
  form.elements['monitor.failure_threshold'].value = cfg.monitor?.failure_threshold || 1
  form.elements['monitor.recovery_threshold'].value = cfg.monitor?.recovery_threshold || 1
  form.elements['monitor.check_mode'].value = cfg.monitor?.check_mode || 'status'
  form.elements['monitor.default_model_id'].value = cfg.monitor?.default_model_id || ''
  form.elements['monitor.notify_on_recovery'].checked = Boolean(cfg.monitor?.notify_on_recovery)
  form.elements['account_defaults.use_proxy'].checked = Boolean(cfg.account_defaults?.use_proxy)
  form.elements['account_defaults.proxy_id'].value = cfg.account_defaults?.proxy_id || ''
  form.elements['account_defaults.priority'].value = cfg.account_defaults?.priority || 8
  form.elements['account_defaults.concurrency'].value = cfg.account_defaults?.concurrency || 30
  form.elements['account_defaults.group_ids'].value = (cfg.account_defaults?.group_ids || []).join(',')
  form.elements['email.enabled'].checked = Boolean(cfg.email?.enabled)
  form.elements['email.smtp_host'].value = cfg.email?.smtp_host || ''
  form.elements['email.smtp_port'].value = cfg.email?.smtp_port || 587
  form.elements['email.username'].value = cfg.email?.username || ''
  form.elements['email.password'].value = cfg.email?.password || ''
  form.elements['email.from'].value = cfg.email?.from || ''
  form.elements['email.from_name'].value = cfg.email?.from_name || ''
  form.elements['email.use_tls'].checked = Boolean(cfg.email?.use_tls)
  form.elements['email.to'].value = (cfg.email?.to || []).join('\n')
}

function readConfigForm() {
  const form = $('#settingsForm')
  return {
    auth_token: form.elements['auth_token'].value.trim(),
    api_key: form.elements['api_key'].value.trim(),
    sub2api: {
      base_url: form.elements['sub2api.base_url'].value.trim(),
      admin_api_key: form.elements['sub2api.admin_api_key'].value.trim(),
      request_timeout_seconds: state.config?.sub2api?.request_timeout_seconds || 20,
      test_timeout_seconds: state.config?.sub2api?.test_timeout_seconds || 120
    },
    monitor: {
      enabled: form.elements['monitor.enabled'].checked,
      check_interval_seconds: Number(form.elements['monitor.check_interval_seconds'].value || 5),
      failure_threshold: Number(form.elements['monitor.failure_threshold'].value || 1),
      recovery_threshold: Number(form.elements['monitor.recovery_threshold'].value || 1),
      check_mode: form.elements['monitor.check_mode'].value,
      default_model_id: form.elements['monitor.default_model_id'].value.trim(),
      notify_on_recovery: form.elements['monitor.notify_on_recovery'].checked
    },
    account_defaults: {
      use_proxy: form.elements['account_defaults.use_proxy'].checked,
      proxy_id: form.elements['account_defaults.proxy_id'].value ? Number(form.elements['account_defaults.proxy_id'].value) : null,
      priority: Number(form.elements['account_defaults.priority'].value || 8),
      concurrency: Number(form.elements['account_defaults.concurrency'].value || 30),
      group_ids: parseCSVNumbers(form.elements['account_defaults.group_ids'].value)
    },
    email: {
      enabled: form.elements['email.enabled'].checked,
      smtp_host: form.elements['email.smtp_host'].value.trim(),
      smtp_port: Number(form.elements['email.smtp_port'].value || 587),
      username: form.elements['email.username'].value.trim(),
      password: form.elements['email.password'].value.trim(),
      from: form.elements['email.from'].value.trim(),
      from_name: form.elements['email.from_name'].value.trim(),
      use_tls: form.elements['email.use_tls'].checked,
      to: parseRecipients(form.elements['email.to'].value)
    }
  }
}

async function bootstrap() {
  if (!state.token) {
    setConnected(false)
    return
  }
  try {
    await Promise.all([loadAccounts(true), loadConfig()])
    setConnected(true)
    startPolling()
  } catch (err) {
    setConnected(false)
  }
}

$('#loginForm').addEventListener('submit', async (event) => {
  event.preventDefault()
  state.token = $('#tokenInput').value.trim()
  localStorage.setItem('monitor_token', state.token)
  try {
    await Promise.all([loadAccounts(), loadConfig()])
    setConnected(true)
    startPolling()
  } catch (err) {
    toast(err.message, true)
  }
})

$('#logoutBtn').addEventListener('click', () => {
  localStorage.removeItem('monitor_token')
  state.token = ''
  setConnected(false)
  stopPolling()
})

$$('.tab').forEach((tab) => tab.addEventListener('click', () => switchView(tab.dataset.view)))

$('#refreshAccountsBtn').addEventListener('click', () => loadAccounts())

$('#accountsBody').addEventListener('click', async (event) => {
  const button = event.target.closest('button[data-action]')
  if (!button) return
  const id = button.dataset.id
  const action = button.dataset.action
  try {
    if (action === 'check') {
      button.disabled = true
      await api(`/api/monitor-accounts/${id}/check`, { method: 'POST' })
      toast('检查完成')
    } else if (action === 'edit') {
      const name = prompt('监控账号名称', button.dataset.name || '')
      if (name === null) return
      await api(`/api/monitor-accounts/${id}`, {
        method: 'PATCH',
        body: JSON.stringify({ name })
      })
      toast('已更新监控账号')
    } else if (action === 'delete') {
      if (!confirm(`移除监控账号 ${id}？`)) return
      await api(`/api/monitor-accounts/${id}`, { method: 'DELETE' })
      toast('已移除')
    }
    await loadAccounts(true)
  } catch (err) {
    toast(err.message, true)
  } finally {
    button.disabled = false
  }
})

$('#accountsBody').addEventListener('change', async (event) => {
  const input = event.target.closest('input[data-action="enabled"]')
  if (!input) return
  const enabled = input.checked
  input.disabled = true
  try {
    await api(`/api/monitor-accounts/${input.dataset.id}`, {
      method: 'PATCH',
      body: JSON.stringify({ enabled })
    })
    toast(enabled ? '已启用监控' : '已暂停监控')
    await loadAccounts(true)
  } catch (err) {
    input.checked = !enabled
    toast(err.message, true)
  } finally {
    input.disabled = false
  }
})

$('#watchExistingForm').addEventListener('submit', async (event) => {
  event.preventDefault()
  const form = event.currentTarget
  const payload = {
    account_name: form.elements['account_name'].value.trim(),
    enabled: form.elements['enabled'].checked
  }
  try {
    await api('/api/monitor-accounts', { method: 'POST', body: JSON.stringify(payload) })
    form.reset()
    form.elements['enabled'].checked = true
    toast('已加入监控')
    switchView('accounts')
    await loadAccounts(true)
  } catch (err) {
    toast(err.message, true)
  }
})

$('#createAccountForm').elements['platform'].addEventListener('change', updateCreateAccountFields)
$('#createAccountForm').elements['type'].addEventListener('change', updateCreateAccountFields)

$('#createAccountForm').addEventListener('submit', async (event) => {
  event.preventDefault()
  const form = event.currentTarget
  try {
    const credentials = parseJSONField(form.elements['credentials'].value, {})
    const extra = parseJSONField(form.elements['extra'].value, {})
    const payload = {
      name: form.elements['name'].value.trim(),
      notes: form.elements['notes'].value.trim() || null,
      platform: form.elements['platform'].value.trim(),
      type: form.elements['type'].value.trim(),
      credentials,
      extra,
      rate_multiplier: optionalNumber(form.elements['rate_multiplier'].value),
      load_factor: optionalNumber(form.elements['load_factor'].value),
      expires_at: datetimeLocalToUnix(form.elements['expires_at'].value),
      confirm_mixed_channel_risk: form.elements['confirm_mixed_channel_risk'].checked,
      model_id: form.elements['model_id'].value.trim(),
      enabled: form.elements['enabled'].checked
    }
    Object.keys(payload).forEach((key) => payload[key] === undefined && delete payload[key])
    await api('/api/monitor-accounts', { method: 'POST', body: JSON.stringify(payload) })
    form.reset()
    form.elements['enabled'].checked = true
    form.elements['platform'].value = 'openai'
    form.elements['type'].value = 'oauth'
    updateCreateAccountFields()
    toast('已创建并加入监控')
    switchView('accounts')
    await loadAccounts(true)
  } catch (err) {
    toast(err.message, true)
  }
})

$('#reloadConfigBtn').addEventListener('click', async () => {
  try {
    await loadConfig()
    toast('配置已重新加载')
  } catch (err) {
    toast(err.message, true)
  }
})

$('#settingsForm').addEventListener('submit', async (event) => {
  event.preventDefault()
  try {
    const payload = readConfigForm()
    const updated = await api('/api/config', { method: 'PUT', body: JSON.stringify(payload) })
    state.config = updated
    fillConfigForm(updated)
    toast('配置已保存')
  } catch (err) {
    toast(err.message, true)
  }
})

updateCreateAccountFields()
bootstrap()
