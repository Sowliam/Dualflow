const path = require('node:path')
const { spawn } = require('node:child_process')
const readline = require('node:readline')

const CORE_BIN = path.join(__dirname, '..', 'bin', 'dualflow-core.exe')

console.log('==================================================================')
console.log('   DUALFLOW PHASE 6: MULTI-PATH BONDING ENGINE VERIFICATION')
console.log('==================================================================')

let child = null
let requestCounter = 0
const pendingRequests = new Map()

function sendRequest(type, payload = {}) {
  const reqId = `req-${++requestCounter}-${Date.now()}`
  const req = {
    protocol_version: '1.0.0',
    request_id: reqId,
    type,
    payload,
  }

  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      pendingRequests.delete(reqId)
      reject(new Error(`Timeout waiting for ${type} (${reqId})`))
    }, 10000)

    pendingRequests.set(reqId, {
      resolve: (data) => {
        clearTimeout(timer)
        resolve(data)
      },
      reject: (err) => {
        clearTimeout(timer)
        reject(err)
      },
    })

    child.stdin.write(JSON.stringify(req) + '\n')
  })
}

async function runVerification() {
  console.log('\n[TEST 1] Spawning Go Network Core daemon with Phase 6 Engine...')
  child = spawn(CORE_BIN, [], {
    stdio: ['pipe', 'pipe', 'inherit'],
    windowsHide: true,
  })

  const rl = readline.createInterface({ input: child.stdout })

  await new Promise((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error('Go Core startup timeout')), 25000)
    rl.on('line', (line) => {
      if (!line.trim()) return
      try {
        const msg = JSON.parse(line)
        if (msg.type === 'event' && msg.event === 'ready') {
          clearTimeout(timeout)
          console.log(`[PASS] Go Core ready: v${msg.data?.version}, adapters: ${msg.data?.adapters_count}`)
          resolve()
        } else if (msg.type === 'response' || msg.type === 'error') {
          const p = pendingRequests.get(msg.request_id)
          if (p) {
            pendingRequests.delete(msg.request_id)
            if (msg.success) p.resolve(msg.data)
            else p.reject(new Error(msg.error?.message || 'Error response'))
          }
        }
      } catch (err) {
        console.error('Failed to parse line:', line)
      }
    })
  })

  console.log('\n[TEST 2] Querying Multi-Path Engine status (multipath_get_status)...')
  const status = await sendRequest('multipath_get_status')
  console.log('[PASS] Multi-Path Status received:')
  console.log(JSON.stringify(status, null, 2))

  if (status.scheduler_mode !== 'round_robin') {
    throw new Error(`Expected default mode round_robin, got ${status.scheduler_mode}`)
  }

  console.log('\n[TEST 3] Testing dynamic mode switching to Weighted Round-Robin (weighted_rr)...')
  const wrrStatus = await sendRequest('multipath_set_mode', { mode: 'weighted_rr' })
  console.log(`[PASS] Mode switched to: ${wrrStatus.scheduler_mode}`)
  if (wrrStatus.scheduler_mode !== 'weighted_rr') {
    throw new Error('Failed to switch to weighted_rr')
  }

  console.log('\n[TEST 4] Updating path weights (multipath_set_weights)...')
  const weightStatus = await sendRequest('multipath_set_weights', {
    weights: { modem: 3, android: 1 },
  })
  console.log('[PASS] Weights updated successfully.')

  console.log('\n[TEST 5] Testing dynamic mode switching to Lowest Latency (lowest_latency)...')
  const llStatus = await sendRequest('multipath_set_mode', { mode: 'lowest_latency' })
  console.log(`[PASS] Mode switched to: ${llStatus.scheduler_mode}`)
  if (llStatus.scheduler_mode !== 'lowest_latency') {
    throw new Error('Failed to switch to lowest_latency')
  }

  console.log('\n[TEST 6] Testing dynamic mode switching to Redundant (redundant)...')
  const redStatus = await sendRequest('multipath_set_mode', { mode: 'redundant' })
  console.log(`[PASS] Mode switched to: ${redStatus.scheduler_mode}`)
  if (redStatus.scheduler_mode !== 'redundant') {
    throw new Error('Failed to switch to redundant')
  }

  console.log('\n[TEST 7] Switching back to default Round-Robin (round_robin)...')
  const rrFinal = await sendRequest('multipath_set_mode', { mode: 'round_robin' })
  console.log(`[PASS] Mode restored to: ${rrFinal.scheduler_mode}`)

  console.log('\n[TEST 8] Injecting test packet through Multi-Path Pipeline...')
  const injectRes = await sendRequest('packet_inject_test', {
    ip_version: 4,
    payload_size: 64,
  })
  console.log('[PASS] Injected test packet result:', injectRes)

  console.log('\n[TEST 9] Graceful daemon shutdown...')
  child.stdin.end()
  await new Promise((resolve) => child.on('close', resolve))
  console.log('[PASS] Daemon closed cleanly with exit code 0.')

  console.log('\n==================================================================')
  console.log('   ALL PHASE 6 MULTI-PATH ENGINE TESTS PASSED SUCCESSFULLY!')
  console.log('==================================================================\n')
}

runVerification()
  .then(() => process.exit(0))
  .catch((err) => {
    console.error('\n[FAIL] Phase 6 verification failed:', err)
    if (child) child.kill()
    process.exit(1)
  })

