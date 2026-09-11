import { createHash, timingSafeEqual } from "node:crypto"
import { createServer, type IncomingMessage, type ServerResponse } from "node:http"
import type { WorkerConfig } from "./config.js"
import { WorkerError, toWorkerError } from "./errors.js"
import type { BrowserManager, StorageState } from "./manager.js"

const PROTOCOL = "1"
const VERSION = "0.1.0"

export function createWorkerServer(config: WorkerConfig, manager: BrowserManager) {
  return createServer((request, response) => {
    void handle(config, manager, request, response)
  })
}

async function handle(
  config: WorkerConfig,
  manager: BrowserManager,
  request: IncomingMessage,
  response: ServerResponse,
): Promise<void> {
  response.setHeader("X-Browser-Protocol", PROTOCOL)
  response.setHeader("Cache-Control", "no-store")
  const url = new URL(request.url ?? "/", "http://worker.local")
  const path = url.pathname
  try {
    if (request.method === "GET" && path === "/healthz") {
      sendJSON(response, 200, { status: "ok", version: VERSION, protocol: 1 })
      return
    }
    if (request.method === "GET" && path === "/readyz") {
      const ready = await manager.ready()
      sendJSON(response, 200, { status: "ready", browser: ready.browser, contexts: ready.contexts })
      return
    }
    if (!authorized(config, request)) {
      throw new WorkerError("unauthorized", "missing or invalid worker token")
    }
    if (request.method === "GET" && path === "/v1/profiles") {
      sendJSON(response, 200, { profiles: await manager.list() })
      return
    }
    const match = /^\/v1\/profiles\/([^/]+)(?:\/([a-z-]+))?$/.exec(path)
    if (!match) {
      throw new WorkerError("not_found", "unknown route")
    }
    const profileID = decodeURIComponent(match[1] ?? "")
    const operation = match[2] ?? ""
    switch (`${request.method} ${operation}`) {
      case "DELETE ": {
        requireEmptyBody(request)
        await manager.close(profileID, url.searchParams.get("purge") === "true")
        sendJSON(response, 200, { closed: true, purged: url.searchParams.get("purge") === "true" })
        return
      }
      case "POST ensure": {
        const body = await readObject(request, config.maxBodyBytes)
        const headless = optionalBoolean(body.headless, "headless")
        sendJSON(response, 200, await manager.ensure(profileID, headless))
        return
      }
      case "GET storage-state": {
        requireEmptyBody(request)
        sendJSON(response, 200, await manager.exportState(profileID))
        return
      }
      case "PUT storage-state": {
        const body = await readObject(request, config.maxBodyBytes)
        await manager.importState(profileID, body as unknown as StorageState)
        sendJSON(response, 200, { imported: true })
        return
      }
      case "POST goto": {
        const body = await readObject(request, config.maxBodyBytes)
        const target = requireString(body.url, "url")
        sendJSON(
          response,
          200,
          await manager.goto(profileID, {
            url: target,
            wait_until: optionalString(body.wait_until, "wait_until"),
            timeout_ms: optionalInteger(body.timeout_ms, "timeout_ms"),
          }),
        )
        return
      }
      case "GET page": {
        requireEmptyBody(request)
        sendJSON(response, 200, await manager.pageInfo(profileID))
        return
      }
      case "POST content": {
        const body = await readObject(request, config.maxBodyBytes)
        sendJSON(response, 200, await manager.content(profileID, { timeout_ms: optionalInteger(body.timeout_ms, "timeout_ms") }))
        return
      }
      case "POST screenshot": {
        const body = await readObject(request, config.maxBodyBytes)
        const screenshot = await manager.screenshot(profileID, {
          selector: optionalString(body.selector, "selector"),
          full_page: optionalBoolean(body.full_page, "full_page"),
          timeout_ms: optionalInteger(body.timeout_ms, "timeout_ms"),
        })
        response.setHeader("Content-Type", "image/png")
        response.writeHead(200)
        response.end(screenshot)
        return
      }
      case "POST locator": {
        const body = await readObject(request, config.maxBodyBytes)
        await manager.locator(profileID, {
          action: requireString(body.action, "action"),
          selector: requireString(body.selector, "selector"),
          value: optionalString(body.value, "value"),
          state: optionalString(body.state, "state"),
          timeout_ms: optionalInteger(body.timeout_ms, "timeout_ms"),
        })
        sendJSON(response, 200, { matched: true })
        return
      }
      default:
        throw new WorkerError("not_found", "unknown route")
    }
  } catch (error) {
    const workerError = toWorkerError(error)
    sendJSON(response, workerError.status, {
      error: { code: workerError.code, message: workerError.message.slice(0, 512) },
    })
  }
}

function authorized(config: WorkerConfig, request: IncomingMessage): boolean {
  const header = request.headers.authorization ?? ""
  const prefix = "Bearer "
  if (!header.startsWith(prefix)) return false
  const provided = createHash("sha256").update(header.slice(prefix.length)).digest()
  const expected = createHash("sha256").update(config.token).digest()
  return timingSafeEqual(provided, expected)
}

async function readObject(request: IncomingMessage, limit: number): Promise<Record<string, unknown>> {
  const body = await readBody(request, limit)
  if (body === undefined) return {}
  let parsed: unknown
  try {
    parsed = JSON.parse(body)
  } catch {
    throw new WorkerError("invalid", "request body must be valid JSON")
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    throw new WorkerError("invalid", "request body must be a JSON object")
  }
  return parsed as Record<string, unknown>
}

async function readBody(request: IncomingMessage, limit: number): Promise<string | undefined> {
  const chunks: Buffer[] = []
  let size = 0
  for await (const chunk of request) {
    const buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    size += buffer.length
    if (size > limit) {
      throw new WorkerError("invalid", "request body is too large")
    }
    chunks.push(buffer)
  }
  if (chunks.length === 0) return undefined
  return Buffer.concat(chunks).toString("utf8")
}

function requireEmptyBody(request: IncomingMessage): void {
  if ((request.headers["content-length"] ?? "0") !== "0") {
    throw new WorkerError("invalid", "request must not contain a body")
  }
}

function requireString(value: unknown, name: string): string {
  if (typeof value !== "string" || value.trim() === "") {
    throw new WorkerError("invalid", `${name} must be a non-empty string`)
  }
  return value.trim()
}

function optionalString(value: unknown, name: string): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== "string") {
    throw new WorkerError("invalid", `${name} must be a string`)
  }
  return value
}

function optionalBoolean(value: unknown, name: string): boolean | undefined {
  if (value === undefined) return undefined
  if (typeof value !== "boolean") {
    throw new WorkerError("invalid", `${name} must be a boolean`)
  }
  return value
}

function optionalInteger(value: unknown, name: string): number | undefined {
  if (value === undefined) return undefined
  if (typeof value !== "number" || !Number.isInteger(value)) {
    throw new WorkerError("invalid", `${name} must be an integer`)
  }
  return value
}

function sendJSON(response: ServerResponse, status: number, value: unknown): void {
  response.setHeader("Content-Type", "application/json")
  response.writeHead(status)
  response.end(JSON.stringify(value))
}
