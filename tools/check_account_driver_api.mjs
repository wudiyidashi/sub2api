// 对隔离的本机开发实例验证真实管理 API，不调用模型上游。
// 设置 ACCOUNT_DRIVER_SMOKE_BASE_URL、ACCOUNT_DRIVER_SMOKE_EMAIL 和
// ACCOUNT_DRIVER_SMOKE_PASSWORD；账号会使用明确的 fixture 名称与无效凭据。
import assert from 'node:assert/strict'
import { randomUUID } from 'node:crypto'

const baseURL = new URL(process.env.ACCOUNT_DRIVER_SMOKE_BASE_URL || 'http://127.0.0.1:18088')
assert(['127.0.0.1', 'localhost', '[::1]'].includes(baseURL.hostname), 'use an isolated loopback development instance')
const email = process.env.ACCOUNT_DRIVER_SMOKE_EMAIL
const password = process.env.ACCOUNT_DRIVER_SMOKE_PASSWORD
const expectEnhanced = process.env.ACCOUNT_DRIVER_SMOKE_EXPECT_ENHANCED === '1'
const expectWebSocket = process.env.ACCOUNT_DRIVER_SMOKE_EXPECT_WEBSOCKET === '1'
assert(!expectWebSocket || expectEnhanced, 'WS smoke requires the enhanced runtime')
assert(email && password, 'set the fixture administrator email and password')

async function request(method, path, body, token) {
  const response = await fetch(new URL(`/api/v1${path}`, baseURL), {
    method,
    headers: {
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(token ? { Authorization: `Bearer ${token}` } : {})
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(15_000)
  })
  const payload = await response.json()
  return { status: response.status, payload }
}

const unauthorized = await request('GET', '/admin/account-drivers')
assert.equal(unauthorized.status, 401)
const login = await request('POST', '/auth/login', { email, password })
assert.equal(login.status, 200, 'fixture login failed')
const token = login.payload.data.access_token
assert(token)
const catalog = await request('GET', '/admin/account-drivers', undefined, token)
assert.equal(catalog.status, 200, 'fixture must already have its required administrator state')
assert.equal(catalog.payload.data.default_driver_id, 'native')
assert.deepEqual(catalog.payload.data.drivers.map(driver => driver.id), expectEnhanced ? ['native', 'codex-rs-http'] : ['native'])
assert.equal(catalog.payload.data.drivers[0].available, true)
if (expectWebSocket) {
  const driver = catalog.payload.data.drivers.find(driver => driver.id === 'codex-rs-http')
  assert(driver.transport_protocols.includes('websocket'), 'runtime did not negotiate WebSocket')
}

const fixture = randomUUID().slice(0, 8)
function accountBody(suffix) {
  return {
    name: `driver-smoke-${fixture}-${suffix}`,
    platform: 'openai', type: 'apikey', concurrency: 3, priority: 50,
    credentials: { api_key: `sk-invalid-driver-fixture-${fixture}-${suffix}`, base_url: 'http://127.0.0.1:19001' },
    group_ids: []
  }
}
const legacy = await request('POST', '/admin/accounts', accountBody('legacy'), token)
assert.equal(legacy.status, 200, 'legacy account creation failed')
assert.equal(legacy.payload.data.account_driver.id, 'native')
assert(legacy.payload.data.account_driver.binding_revision >= 1)
const explicit = await request('POST', '/admin/accounts', {
  ...accountBody('explicit'), account_driver_id: 'native', driver_settings: {}
}, token)
assert.equal(explicit.status, 200, 'explicit native creation failed')
assert.equal(explicit.payload.data.account_driver.id, 'native')
const accountID = explicit.payload.data.id

for (const [method, path, body, reason] of [
  ['GET', '/admin/accounts?account_driver_id=codex-enhanced', undefined, 'ACCOUNT_DRIVER_UNKNOWN'],
  ['POST', '/admin/accounts', { ...accountBody('rejected'), account_driver_id: 'codex-enhanced' }, 'ACCOUNT_DRIVER_UNKNOWN'],
  ['POST', '/admin/accounts', { ...accountBody('settings'), driver_settings: { mode: 'unsupported' } }, 'ACCOUNT_DRIVER_SETTINGS_UNSUPPORTED'],
  ['PUT', `/admin/accounts/${accountID}`, { name: 'must-not-change', account_driver_id: 'codex-enhanced' }, 'ACCOUNT_DRIVER_UNKNOWN'],
  ['POST', '/admin/accounts/bulk-update', { filters: { account_driver_id: 'codex-enhanced' }, status: 'inactive' }, 'ACCOUNT_DRIVER_UNKNOWN']
]) {
  const result = await request(method, path, body, token)
  assert.equal(result.status, 400, `${method} ${path} must reject unsupported driver input`)
  assert.equal(result.payload.reason, reason)
}
const detail = await request('GET', `/admin/accounts/${accountID}`, undefined, token)
assert.equal(detail.payload.data.name, accountBody('explicit').name, 'rejected edit mutated the account')
const listed = await request('GET', `/admin/accounts?account_driver_id=native&lite=1&search=driver-smoke-${fixture}`, undefined, token)
assert.equal(listed.status, 200)
assert.equal(listed.payload.data.items.length, 2, 'rejected requests created or hid a fixture account')
assert(listed.payload.data.items.every(account => account.account_driver.id === 'native'))
assert.equal(listed.payload.data.items[0].credentials?.api_key, undefined, 'lite response revealed a credential')

if (expectEnhanced) {
  const enhanced = await request('POST', '/admin/accounts', {
    ...accountBody('enhanced'), type: 'oauth', account_driver_id: 'codex-rs-http',
    ...(expectWebSocket ? { extra: { openai_oauth_responses_websockets_v2_mode: 'ctx_pool' } } : {}),
    credentials: {
      access_token: 'invalid-fixture-oauth-token-no-model-calls',
      chatgpt_account_id: 'driver-fixture-only',
      expires_at: new Date(Date.now() + 86_400_000).toISOString()
    }
  }, token)
  assert.equal(enhanced.status, 200, 'ready enhanced OAuth creation failed')
  assert.equal(enhanced.payload.data.account_driver.id, 'codex-rs-http')
  assert.equal(enhanced.payload.data.account_driver.lifecycle_driver_id, 'native')
  assert.equal(enhanced.payload.data.account_driver.binding_revision, 1)
  if (expectWebSocket) {
    assert(enhanced.payload.data.account_driver.transport_protocols.includes('websocket'))
    assert.equal(enhanced.payload.data.extra.openai_oauth_responses_websockets_v2_mode, 'ctx_pool')
    const edit = await request('PUT', `/admin/accounts/${enhanced.payload.data.id}`, { notes: 'WS capability smoke ordinary edit' }, token)
    assert.equal(edit.status, 200)
    assert.equal(edit.payload.data.extra.openai_oauth_responses_websockets_v2_mode, 'ctx_pool')
    assert.equal(edit.payload.data.account_driver.binding_revision, 1)
  }
  const filtered = await request('GET', `/admin/accounts?account_driver_id=codex-rs-http&lite=1&search=driver-smoke-${fixture}`, undefined, token)
  assert.equal(filtered.status, 200)
  assert.equal(filtered.payload.data.items.length, 1)
  assert.equal(filtered.payload.data.items[0].id, enhanced.payload.data.id)
  const nativeFiltered = await request('GET', `/admin/accounts?account_driver_id=native&search=driver-smoke-${fixture}`, undefined, token)
  assert.equal(nativeFiltered.payload.data.items.length, 2)
  const switchAttempt = await request('PUT', `/admin/accounts/${enhanced.payload.data.id}`, { account_driver_id: 'native' }, token)
  assert.equal(switchAttempt.status, 400)
  assert.equal(switchAttempt.payload.reason, 'ACCOUNT_DRIVER_SWITCH_UNSUPPORTED')
  const invalidKind = await request('POST', '/admin/accounts', { ...accountBody('enhanced-apikey-rejected'), account_driver_id: 'codex-rs-http' }, token)
  assert.equal(invalidKind.status, 400)
  assert.equal(invalidKind.payload.reason, 'ACCOUNT_DRIVER_ACCOUNT_UNSUPPORTED')
  console.log(`PASS: ready enhanced creation, native lifecycle, persisted binding, SQL pagination/count isolation and switch/auth-kind refusal; enhanced fixture ID=${enhanced.payload.data.id}`)
}

console.log('PASS: auth guard, capability catalog, legacy/native creation, lite filtering, unsupported driver/settings/edit/bulk rejection and no rejected mutation')
console.log(`Fixture accounts: driver-smoke-${fixture}-legacy / driver-smoke-${fixture}-explicit (invalid upstream credentials; no model calls)`)
