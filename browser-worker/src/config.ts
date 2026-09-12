export type WorkerConfig = {
  host: string
  port: number
  token: string
  dataDir: string
  headless: boolean
  maxInFlight: number
  maxTimeoutMs: number
  maxBodyBytes: number
  channel?: string
  executablePath?: string
}

function parseInteger(env: NodeJS.ProcessEnv, name: string, fallback: number, minimum: number, maximum: number): number {
  const raw = env[name]
  if (raw === undefined || raw.trim() === "") return fallback
  const value = Number.parseInt(raw, 10)
  if (!Number.isFinite(value) || value < minimum || value > maximum) {
    throw new Error(`${name} must be an integer between ${minimum} and ${maximum}`)
  }
  return value
}

function parseBoolean(env: NodeJS.ProcessEnv, name: string, fallback: boolean): boolean {
  const raw = env[name]
  if (raw === undefined || raw.trim() === "") return fallback
  return ["1", "true", "yes", "on"].includes(raw.trim().toLowerCase())
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): WorkerConfig {
  const token = (env.BROWSER_WORKER_TOKEN ?? "").trim()
  if (!token) {
    throw new Error("BROWSER_WORKER_TOKEN is required")
  }
  const channel = (env.BROWSER_WORKER_CHANNEL ?? "").trim()
  const executablePath = (env.BROWSER_WORKER_EXECUTABLE ?? "").trim()
  return {
    host: (env.BROWSER_WORKER_HOST ?? "127.0.0.1").trim(),
    port: parseInteger(env, "BROWSER_WORKER_PORT", 8088, 1, 65535),
    token,
    dataDir: (env.BROWSER_WORKER_DATA_DIR ?? "/data/browser-profiles").trim(),
    headless: parseBoolean(env, "BROWSER_WORKER_HEADLESS", true),
    maxInFlight: parseInteger(env, "BROWSER_WORKER_MAX_IN_FLIGHT", 4, 1, 64),
    maxTimeoutMs: parseInteger(env, "BROWSER_WORKER_MAX_TIMEOUT_MS", 120_000, 1_000, 600_000),
    maxBodyBytes: parseInteger(env, "BROWSER_WORKER_MAX_BODY_BYTES", 8 << 20, 4 << 10, 64 << 20),
    ...(channel ? { channel } : {}),
    ...(executablePath ? { executablePath } : {}),
  }
}
