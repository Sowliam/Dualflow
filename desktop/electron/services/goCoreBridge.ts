import { spawn, type ChildProcess } from 'node:child_process'
import path from 'node:path'
import fs from 'node:fs'
import readline from 'node:readline'
import { EventEmitter } from 'node:events'
import { fileURLToPath } from 'node:url'
import type { NetworkAdapter, PathTelemetryUpdate } from '../../src/types'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

export interface GoCoreStatus {
  isRunning: boolean
  pid: number | null
  version: string
  uptimeMs: number
  path1State: string
  path2State: string
}

export interface GoCorePaths {
  path1: any
  path2: any
}

export class GoCoreBridge extends EventEmitter {
  private child: ChildProcess | null = null
  private isReady = false
  private pendingRequests = new Map<
    string,
    { resolve: (data: any) => void; reject: (err: Error) => void; timer: NodeJS.Timeout }
  >()
  private requestCounter = 0
  private restartAttempts = 0
  private maxRestarts = 3
  private isShuttingDown = false
  private corePath: string

  constructor(customPath?: string) {
    super()
    this.corePath = customPath || this.resolveCorePath()
  }

  public resolveCorePath(): string {
    const appRoot = process.env.APP_ROOT || path.join(__dirname, '..')
    const candidates = [
      path.join(appRoot, 'bin', 'dualflow-core.exe'),
      path.join(appRoot, 'network-core', 'dualflow-core.exe'),
      path.join(process.resourcesPath || '', 'bin', 'dualflow-core.exe'),
      path.join(appRoot, 'resources', 'bin', 'dualflow-core.exe'),
    ]

    for (const c of candidates) {
      if (fs.existsSync(c)) {
        return c
      }
    }

    // Default target
    return path.join(appRoot, 'network-core', 'dualflow-core.exe')
  }

  public async start(): Promise<boolean> {
    if (this.child && !this.child.killed) {
      console.log('[GoCoreBridge] Go core already running (PID:', this.child.pid, ')')
      return true
    }

    if (!fs.existsSync(this.corePath)) {
      throw new Error(`dualflow-core.exe binary not found at ${this.corePath}`)
    }

    console.log('[GoCoreBridge] Spawning Go Network Core daemon from:', this.corePath)
    this.isShuttingDown = false

    return new Promise((resolve, reject) => {
      const startupTimer = setTimeout(() => {
        if (!this.isReady) {
          reject(new Error('Go Network Core failed to signal ready state within 10 seconds'))
        }
      }, 10000)

      try {
        this.child = spawn(this.corePath, [], {
          stdio: ['pipe', 'pipe', 'pipe'],
          windowsHide: true,
          env: { ...process.env },
        })

        if (!this.child.pid) {
          clearTimeout(startupTimer)
          return reject(new Error('Failed to spawn Go Network Core child process'))
        }

        console.log('[GoCoreBridge] Go Network Core spawned with PID:', this.child.pid)

        // Stderr logging
        this.child.stderr?.on('data', (chunk) => {
          const lines = chunk.toString().trim().split(/\r?\n/)
          for (const l of lines) {
            console.log(`[GoCore:Stderr] ${l}`)
          }
        })

        // Stdout NDJSON parsing
        if (this.child.stdout) {
          const rl = readline.createInterface({ input: this.child.stdout })
          rl.on('line', (line) => {
            const raw = line.trim()
            if (!raw) return
            try {
              const msg = JSON.parse(raw)
              this.handleProtocolMessage(msg)
            } catch (err) {
              console.warn('[GoCoreBridge] Unparseable stdout from Go Core:', raw, err)
            }
          })
        }

        // Process exit handling
        this.child.on('exit', (code, signal) => {
          console.warn(`[GoCoreBridge] Go Core process exited (code: ${code}, signal: ${signal})`)
          this.isReady = false
          this.child = null

          // Reject any pending requests
          for (const [id, req] of this.pendingRequests.entries()) {
            clearTimeout(req.timer)
            req.reject(new Error(`Go Core exited before replying to request ${id}`))
          }
          this.pendingRequests.clear()

          if (!this.isShuttingDown && this.restartAttempts < this.maxRestarts) {
            this.restartAttempts++
            console.log(`[GoCoreBridge] Attempting automatic restart (${this.restartAttempts}/${this.maxRestarts})...`)
            setTimeout(() => this.start().catch(console.error), 1000 * this.restartAttempts)
          } else if (this.restartAttempts >= this.maxRestarts) {
            console.error('[GoCoreBridge] Maximum restart attempts exceeded. Core halted.')
            this.emit('crash', { code, signal })
          }
        })

        this.child.on('error', (err) => {
          console.error('[GoCoreBridge] Process error:', err)
          clearTimeout(startupTimer)
          reject(err)
        })

        // Listen for internal ready event
        this.once('ready', () => {
          clearTimeout(startupTimer)
          this.isReady = true
          this.restartAttempts = 0
          console.log('[GoCoreBridge] Go Network Core successfully initialized and READY.')
          resolve(true)
        })
      } catch (err) {
        clearTimeout(startupTimer)
        reject(err)
      }
    })
  }

  public async stop(): Promise<void> {
    this.isShuttingDown = true
    if (!this.child || this.child.killed) {
      return
    }

    console.log('[GoCoreBridge] Gracefully terminating Go Network Core process...')
    return new Promise((resolve) => {
      const killTimer = setTimeout(() => {
        if (this.child && !this.child.killed) {
          console.warn('[GoCoreBridge] Force-killing unresponsive Go core...')
          this.child.kill('SIGKILL')
        }
        resolve()
      }, 2000)

      this.child?.once('exit', () => {
        clearTimeout(killTimer)
        resolve()
      })

      // Close stdin to prompt clean shutdown
      try {
        this.child?.stdin?.end()
      } catch {
        this.child?.kill('SIGTERM')
      }
    })
  }

  public getIsReady(): boolean {
    return this.isReady
  }

  public async sendRequest<T = any>(type: string, payload?: any): Promise<T> {
    if (!this.child || !this.child.stdin || !this.isReady) {
      throw new Error(`Cannot send request '${type}': Go Core is not ready or running.`)
    }

    const requestId = `req-${++this.requestCounter}-${Date.now()}`
    const req = {
      protocol_version: '1.0.0',
      request_id: requestId,
      type,
      payload,
    }

    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pendingRequests.delete(requestId)
        reject(new Error(`Timeout waiting for Go Core response to ${type} (${requestId})`))
      }, 12000)

      this.pendingRequests.set(requestId, {
        resolve: (data) => {
          clearTimeout(timer)
          resolve(data)
        },
        reject: (err) => {
          clearTimeout(timer)
          reject(err)
        },
        timer,
      })

      const raw = JSON.stringify(req) + '\n'
      this.child?.stdin?.write(raw, (err) => {
        if (err) {
          clearTimeout(timer)
          this.pendingRequests.delete(requestId)
          reject(err)
        }
      })
    })
  }

  public async getStatus(): Promise<GoCoreStatus> {
    return this.sendRequest<GoCoreStatus>('get_status')
  }

  public async getAdapters(): Promise<NetworkAdapter[]> {
    return this.sendRequest<NetworkAdapter[]>('get_adapters')
  }

  public async getPaths(): Promise<GoCorePaths> {
    return this.sendRequest<GoCorePaths>('get_paths')
  }

  public async rescan(): Promise<{ adapters: NetworkAdapter[]; path1: any; path2: any }> {
    return this.sendRequest('rescan')
  }

  public async subscribe(): Promise<{ subscribed: boolean }> {
    return this.sendRequest('subscribe')
  }

  public async assignPath(pathId: 'modem' | 'android', adapterId: string): Promise<GoCorePaths> {
    return this.sendRequest('assign_path', { path_id: pathId, adapter_id: adapterId })
  }

  // Wintun Virtual Adapter Methods
  public async wintunGetStatus(): Promise<any> {
    return this.sendRequest('wintun_get_status')
  }

  public async wintunGetStats(): Promise<any> {
    return this.sendRequest('wintun_get_stats')
  }

  public async wintunCreateAdapter(): Promise<any> {
    return this.sendRequest('wintun_create_adapter')
  }

  public async wintunOpenAdapter(): Promise<any> {
    return this.sendRequest('wintun_open_adapter')
  }

  public async wintunStartSession(capacity?: number): Promise<any> {
    return this.sendRequest('wintun_start_session', { capacity })
  }

  public async wintunStopSession(): Promise<any> {
    return this.sendRequest('wintun_stop_session')
  }

  public async wintunCloseAdapter(): Promise<any> {
    return this.sendRequest('wintun_close_adapter')
  }

  public async wintunDeleteAdapter(confirm: boolean): Promise<any> {
    return this.sendRequest('wintun_delete_adapter', { confirm })
  }

  public async wintunSendTestPacket(payloadSize?: number): Promise<any> {
    return this.sendRequest('wintun_send_test_packet', { payload_size: payloadSize })
  }

  // Phase 5: Packet Processing Engine & Transport Protocol Methods
  public async packetGetStatus(): Promise<any> {
    return this.sendRequest('packet_get_status')
  }

  public async packetGetStats(): Promise<any> {
    return this.sendRequest('packet_get_stats')
  }

  public async packetSetTestMode(enabled: boolean): Promise<any> {
    return this.sendRequest('packet_set_test_mode', { enabled })
  }

  public async packetInjectTest(options?: { ipVersion?: number; payloadSize?: number; corrupt?: boolean }): Promise<any> {
    return this.sendRequest('packet_inject_test', {
      ip_version: options?.ipVersion ?? 4,
      payload_size: options?.payloadSize ?? 32,
      corrupt: options?.corrupt ?? false,
    })
  }

  public async packetResetStats(): Promise<any> {
    return this.sendRequest('packet_reset_stats')
  }

  // Phase 6: Multi-Path Bonding Engine Methods
  public async multipathGetStatus(): Promise<any> {
    return this.sendRequest('multipath_get_status')
  }

  public async multipathSetMode(mode: string): Promise<any> {
    return this.sendRequest('multipath_set_mode', { mode })
  }

  public async multipathSetWeights(weights: Record<string, number>): Promise<any> {
    return this.sendRequest('multipath_set_weights', { weights })
  }

  private handleProtocolMessage(msg: any): void {
    if (!msg || typeof msg !== 'object') return

    // Handle responses
    if (msg.type === 'response' || msg.type === 'error') {
      const pending = this.pendingRequests.get(msg.request_id)
      if (pending) {
        this.pendingRequests.delete(msg.request_id)
        if (msg.success) {
          pending.resolve(msg.data)
        } else {
          pending.reject(new Error(msg.error?.message || 'Go core returned error response'))
        }
      }
      return
    }

    // Handle asynchronous push events
    if (msg.type === 'event') {
      if (msg.event === 'ready') {
        this.emit('ready', msg.data)
      } else if (msg.event === 'telemetry_update') {
        this.emit('telemetry_update', msg.data)
      } else if (msg.event === 'adapters_update') {
        this.emit('adapters_update', msg.data)
      }
    }
  }
}

