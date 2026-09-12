import assert from "node:assert/strict"
import { test } from "node:test"
import { loadConfig } from "./config.js"

test("config requires a token", () => {
  assert.throws(() => loadConfig({}), /BROWSER_WORKER_TOKEN/)
  assert.throws(() => loadConfig({ BROWSER_WORKER_TOKEN: "  " }), /BROWSER_WORKER_TOKEN/)
})

test("config applies defaults", () => {
  const config = loadConfig({ BROWSER_WORKER_TOKEN: "secret" })
  assert.equal(config.host, "127.0.0.1")
  assert.equal(config.port, 8088)
  assert.equal(config.dataDir, "/data/browser-profiles")
  assert.equal(config.headless, true)
  assert.equal(config.maxInFlight, 4)
  assert.equal(config.maxTimeoutMs, 120_000)
  assert.equal(config.channel, undefined)
  assert.equal(config.executablePath, undefined)
})

test("config parses overrides", () => {
  const config = loadConfig({
    BROWSER_WORKER_TOKEN: "secret",
    BROWSER_WORKER_HOST: "0.0.0.0",
    BROWSER_WORKER_PORT: "9090",
    BROWSER_WORKER_DATA_DIR: "/tmp/profiles",
    BROWSER_WORKER_HEADLESS: "0",
    BROWSER_WORKER_MAX_IN_FLIGHT: "8",
    BROWSER_WORKER_MAX_TIMEOUT_MS: "30000",
    BROWSER_WORKER_CHANNEL: "chrome",
    BROWSER_WORKER_EXECUTABLE: "/usr/bin/google-chrome-stable",
  })
  assert.equal(config.host, "0.0.0.0")
  assert.equal(config.port, 9090)
  assert.equal(config.dataDir, "/tmp/profiles")
  assert.equal(config.headless, false)
  assert.equal(config.maxInFlight, 8)
  assert.equal(config.maxTimeoutMs, 30_000)
  assert.equal(config.channel, "chrome")
  assert.equal(config.executablePath, "/usr/bin/google-chrome-stable")
})

test("config rejects out-of-range integers", () => {
  assert.throws(() => loadConfig({ BROWSER_WORKER_TOKEN: "secret", BROWSER_WORKER_PORT: "70000" }), /BROWSER_WORKER_PORT/)
  assert.throws(() => loadConfig({ BROWSER_WORKER_TOKEN: "secret", BROWSER_WORKER_MAX_IN_FLIGHT: "0" }), /MAX_IN_FLIGHT/)
  assert.throws(() => loadConfig({ BROWSER_WORKER_TOKEN: "secret", BROWSER_WORKER_MAX_TIMEOUT_MS: "10" }), /MAX_TIMEOUT_MS/)
})
