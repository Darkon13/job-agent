import assert from "node:assert/strict"
import { after, before, test } from "node:test"
import type { AddressInfo } from "node:net"
import { BrowserManager } from "./manager.js"
import { createWorkerServer } from "./server.js"
import { DEFAULT_USER_AGENT, type WorkerConfig } from "./config.js"

const config: WorkerConfig = {
  host: "127.0.0.1",
  port: 0,
  token: "secret",
  dataDir: "/tmp/job-agent-browser-worker-test",
  headless: true,
  userAgent: DEFAULT_USER_AGENT,
  maxInFlight: 2,
  maxTimeoutMs: 5_000,
  maxBodyBytes: 4_096,
}

const manager = new BrowserManager(config)
const server = createWorkerServer(config, manager)
let base = ""

before(async () => {
  await new Promise<void>((resolve) => {
    server.listen(0, "127.0.0.1", resolve)
  })
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`
})

after(async () => {
  await new Promise<void>((resolve) => {
    server.close(() => resolve())
  })
  await manager.shutdown()
})

async function call(path: string, init?: RequestInit): Promise<Response> {
  return fetch(`${base}${path}`, init)
}

function authorized(init: RequestInit = {}): RequestInit {
  return {
    ...init,
    headers: { Authorization: "Bearer secret", "Content-Type": "application/json", ...(init.headers ?? {}) },
  }
}

test("health and readiness are open", async () => {
  const health = await call("/healthz")
  assert.equal(health.status, 200)
  assert.equal(health.headers.get("x-browser-protocol"), "1")
  const body = (await health.json()) as { status: string; protocol: number }
  assert.equal(body.status, "ok")
  assert.equal(body.protocol, 1)

  const ready = await call("/readyz")
  assert.equal(ready.status, 200)
  const readyBody = (await ready.json()) as { status: string; browser: boolean; contexts: number }
  assert.equal(readyBody.status, "ready")
  assert.equal(readyBody.browser, false)
  assert.equal(readyBody.contexts, 0)
})

test("api requires the bearer token", async () => {
  const response = await call("/v1/profiles")
  assert.equal(response.status, 401)
  const body = (await response.json()) as { error: { code: string } }
  assert.equal(body.error.code, "unauthorized")
})

test("api lists profiles with the token", async () => {
  const response = await call("/v1/profiles", authorized())
  assert.equal(response.status, 200)
  assert.deepEqual(await response.json(), { profiles: [] })
})

test("invalid profile ids and locator actions are rejected before the browser", async () => {
  const badProfile = await call("/v1/profiles/bad%21/ensure", authorized({ method: "POST", body: "{}" }))
  assert.equal(badProfile.status, 400)
  const badProfileBody = (await badProfile.json()) as { error: { code: string } }
  assert.equal(badProfileBody.error.code, "invalid")

  const badAction = await call(
    "/v1/profiles/primary/locator",
    authorized({ method: "POST", body: JSON.stringify({ action: "evaluate", selector: "body" }) }),
  )
  assert.equal(badAction.status, 400)
  const badActionBody = (await badAction.json()) as { error: { code: string } }
  assert.equal(badActionBody.error.code, "invalid")
})

test("invalid json and oversized bodies are rejected", async () => {
  const malformed = await call("/v1/profiles/primary/ensure", authorized({ method: "POST", body: "{" }))
  assert.equal(malformed.status, 400)

  const oversized = await call(
    "/v1/profiles/primary/ensure",
    authorized({ method: "POST", body: JSON.stringify({ padding: "x".repeat(config.maxBodyBytes) }) }),
  )
  assert.equal(oversized.status, 400)
})

test("unknown routes return not_found", async () => {
  const response = await call("/v1/nope", authorized())
  assert.equal(response.status, 404)
})
