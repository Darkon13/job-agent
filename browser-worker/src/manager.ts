import { chromium, type Browser, type BrowserContext, type Page } from "playwright"
import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises"
import { join } from "node:path"
import { WorkerError } from "./errors.js"
import type { WorkerConfig } from "./config.js"

export type StorageState = Awaited<ReturnType<BrowserContext["storageState"]>>

export type ContextInfo = {
  profile_id: string
  created: boolean
  pages: number
  headless: boolean
}

export type GotoRequest = {
  url: string
  wait_until?: string
  timeout_ms?: number
}

export type GotoResult = { url: string; status: number; title: string }

export type PageInfo = { url: string; title: string; has_page: boolean }

export type ContentRequest = { timeout_ms?: number }

export type ScreenshotRequest = { selector?: string; full_page?: boolean; timeout_ms?: number }

export type LocatorRequest = {
  action: string
  selector: string
  value?: string
  state?: string
  timeout_ms?: number
}

type ProfileState = {
  context: BrowserContext
  headless: boolean
}

const PROFILE_PATTERN = /^[A-Za-z0-9._-]{1,128}$/
const VIEWPORT = { width: 1440, height: 900 }
const DEFAULT_WAIT_MS = 1_000

export class BrowserManager {
  private readonly browsers = new Map<boolean, Promise<Browser>>()
  private readonly profiles = new Map<string, ProfileState>()
  private readonly locks = new Map<string, Promise<void>>()
  private active = 0
  private readonly waiting: Array<() => void> = []

  constructor(private readonly config: WorkerConfig) {}

  async ready(): Promise<{ browser: boolean; contexts: number }> {
    return { browser: this.browsers.size > 0, contexts: this.profiles.size }
  }

  async ensure(profileID: string, headlessRequest?: boolean): Promise<ContextInfo> {
    const directory = this.profileDir(profileID)
    const headless = headlessRequest ?? this.config.headless
    return this.withLock(profileID, () =>
      this.withSlot(async () => {
        const existing = this.profiles.get(profileID)
        if (existing) {
          return { profile_id: profileID, created: false, pages: existing.context.pages().length, headless: existing.headless }
        }
        await mkdir(directory, { recursive: true, mode: 0o700 })
        const browser = await this.browser(headless)
        const storageState = await this.readState(profileID)
        const context = await browser.newContext({
          viewport: VIEWPORT,
          locale: "ru-RU",
          userAgent: this.config.userAgent,
          storageState: storageState ?? undefined,
        })
        this.profiles.set(profileID, { context, headless })
        return { profile_id: profileID, created: true, pages: context.pages().length, headless }
      }),
    )
  }

  async list(): Promise<ContextInfo[]> {
    return [...this.profiles.entries()].map(([profileID, state]) => ({
      profile_id: profileID,
      created: false,
      pages: state.context.pages().length,
      headless: state.headless,
    }))
  }

  async close(profileID: string, purge: boolean): Promise<void> {
    const directory = this.profileDir(profileID)
    await this.withLock(profileID, () =>
      this.withSlot(async () => {
        const state = this.profiles.get(profileID)
        if (state) {
          await this.persistState(profileID, state)
          await state.context.close()
          this.profiles.delete(profileID)
        }
        if (purge) {
          await rm(directory, { recursive: true, force: true })
        }
      }),
    )
  }

  async exportState(profileID: string): Promise<StorageState> {
    return this.withLock(profileID, () =>
      this.withSlot(async () => {
        const state = await this.requireContext(profileID)
        const storage = await state.context.storageState()
        await this.persistStorage(profileID, storage)
        return storage
      }),
    )
  }

  async importState(profileID: string, storage: StorageState): Promise<void> {
    this.profileDir(profileID)
    if (!isStorageState(storage)) {
      throw new WorkerError("invalid", "storage state requires cookies and origins arrays")
    }
    await this.withLock(profileID, () =>
      this.withSlot(async () => {
        const existing = this.profiles.get(profileID)
        if (existing) {
          await existing.context.close()
          this.profiles.delete(profileID)
        }
        await this.persistStorage(profileID, storage)
        const browser = await this.browser(this.config.headless)
        const context = await browser.newContext({
          viewport: VIEWPORT,
          locale: "ru-RU",
          userAgent: this.config.userAgent,
          storageState: storage,
        })
        this.profiles.set(profileID, { context, headless: this.config.headless })
      }),
    )
  }

  async goto(profileID: string, request: GotoRequest): Promise<GotoResult> {
    const target = this.httpURL(request.url)
    const waitUntil = request.wait_until ?? "domcontentloaded"
    if (!["load", "domcontentloaded", "networkidle"].includes(waitUntil)) {
      throw new WorkerError("invalid", `unsupported wait_until ${waitUntil}`)
    }
    const timeout = this.timeout(request.timeout_ms)
    return this.withLock(profileID, () =>
      this.withSlot(async () => {
        const page = await this.page(profileID)
        const response = await page.goto(target, { waitUntil: waitUntil as "load" | "domcontentloaded" | "networkidle", timeout })
        return { url: page.url(), status: response?.status() ?? 0, title: await page.title() }
      }),
    )
  }

  async pageInfo(profileID: string): Promise<PageInfo> {
    return this.withLock(profileID, () =>
      this.withSlot(async () => {
        const state = this.profiles.get(profileID)
        if (!state) return { url: "", title: "", has_page: false }
        const pages = openPages(state)
        if (pages.length === 0) return { url: "", title: "", has_page: false }
        const page = pages[0]
        return { url: page.url(), title: await page.title(), has_page: true }
      }),
    )
  }

  async content(profileID: string, request: ContentRequest): Promise<{ html: string; url: string }> {
    const timeout = this.timeout(request.timeout_ms)
    return this.withLock(profileID, () =>
      this.withSlot(async () => {
        const page = await this.page(profileID)
        await page.waitForLoadState("domcontentloaded", { timeout }).catch(() => undefined)
        return { html: await page.content(), url: page.url() }
      }),
    )
  }

  async screenshot(profileID: string, request: ScreenshotRequest): Promise<Buffer> {
    const timeout = this.timeout(request.timeout_ms)
    const selector = (request.selector ?? "").trim()
    return this.withLock(profileID, () =>
      this.withSlot(async () => {
        const page = await this.page(profileID)
        if (request.full_page) {
          return page.screenshot({ fullPage: true, timeout })
        }
        if (selector) {
          const locator = page.locator(selector).first()
          await locator.waitFor({ state: "visible", timeout })
          return locator.screenshot({ timeout })
        }
        return page.screenshot({ timeout })
      }),
    )
  }

  async locator(profileID: string, request: LocatorRequest): Promise<void> {
    const selector = (request.selector ?? "").trim()
    if (!selector) {
      throw new WorkerError("invalid", "locator requires a selector")
    }
    if (!["click", "fill", "press", "wait"].includes(request.action)) {
      throw new WorkerError("invalid", `unsupported locator action ${request.action}`)
    }
    const value = request.value ?? ""
    if (request.action === "press" && !value) {
      throw new WorkerError("invalid", "press requires a key value")
    }
    const state = request.state ?? "visible"
    if (request.action === "wait" && !["attached", "detached", "visible", "hidden"].includes(state)) {
      throw new WorkerError("invalid", `unsupported wait state ${state}`)
    }
    const timeout = this.timeout(request.timeout_ms)
    await this.withLock(profileID, () =>
      this.withSlot(async () => {
        const page = await this.page(profileID)
        const locator = page.locator(selector).first()
        switch (request.action) {
          case "click":
            await locator.click({ timeout })
            return
          case "fill":
            await locator.fill(value, { timeout })
            return
          case "press":
            await locator.press(value, { timeout })
            return
          default:
            await locator.waitFor({ state: state as "attached" | "detached" | "visible" | "hidden", timeout })
            return
        }
      }),
    )
  }

  async shutdown(): Promise<void> {
    for (const [profileID, state] of this.profiles) {
      await this.persistState(profileID, state).catch(() => undefined)
      await state.context.close().catch(() => undefined)
    }
    this.profiles.clear()
    for (const browser of this.browsers.values()) {
      await browser.then((instance) => instance.close()).catch(() => undefined)
    }
    this.browsers.clear()
  }

  private browser(headless: boolean): Promise<Browser> {
    let instance = this.browsers.get(headless)
    if (!instance) {
      instance = chromium.launch({
        headless,
        ...(this.config.executablePath ? { executablePath: this.config.executablePath } : {}),
        ...(this.config.channel ? { channel: this.config.channel } : {}),
      })
      this.browsers.set(headless, instance)
    }
    return instance
  }

  private async requireContext(profileID: string): Promise<ProfileState> {
    const state = this.profiles.get(profileID)
    if (!state) {
      throw new WorkerError("not_found", `profile ${profileID} has no browser context`)
    }
    return state
  }

  private async page(profileID: string): Promise<Page> {
    const state = await this.requireContext(profileID)
    const pages = openPages(state)
    if (pages.length > 0) return pages[0]
    return state.context.newPage()
  }

  private profileDir(profileID: string): string {
    if (!PROFILE_PATTERN.test(profileID)) {
      throw new WorkerError("invalid", "profile id must match [A-Za-z0-9._-]{1,128}")
    }
    return join(this.config.dataDir, profileID)
  }

  private statePath(profileID: string): string {
    return join(this.profileDir(profileID), "storage-state.json")
  }

  private async readState(profileID: string): Promise<StorageState | undefined> {
    try {
      const raw = await readFile(this.statePath(profileID), "utf8")
      const parsed = JSON.parse(raw) as unknown
      return isStorageState(parsed) ? parsed : undefined
    } catch {
      return undefined
    }
  }

  private async persistState(profileID: string, state: ProfileState): Promise<void> {
    const storage = await state.context.storageState()
    await this.persistStorage(profileID, storage)
  }

  private async persistStorage(profileID: string, storage: StorageState): Promise<void> {
    const path = this.statePath(profileID)
    await mkdir(this.profileDir(profileID), { recursive: true, mode: 0o700 })
    const temporary = `${path}.tmp-${process.pid}-${Date.now()}`
    await writeFile(temporary, JSON.stringify(storage), { mode: 0o600 })
    await rename(temporary, path)
  }

  private httpURL(value: string): string {
    let url: URL
    try {
      url = new URL(value)
    } catch {
      throw new WorkerError("invalid", "goto requires an absolute URL")
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") {
      throw new WorkerError("invalid", "goto requires an http(s) URL")
    }
    return url.toString()
  }

  private timeout(value?: number): number {
    if (value === undefined || value === 0) return this.config.maxTimeoutMs
    if (!Number.isFinite(value) || value < 0) {
      throw new WorkerError("invalid", "timeout_ms must be a non-negative integer")
    }
    return Math.min(value, this.config.maxTimeoutMs)
  }

  private async withLock<T>(profileID: string, fn: () => Promise<T>, waitMs = DEFAULT_WAIT_MS): Promise<T> {
    const previous = this.locks.get(profileID) ?? Promise.resolve()
    let release!: () => void
    const current = new Promise<void>((resolve) => {
      release = resolve
    })
    this.locks.set(profileID, previous.then(() => current))
    try {
      await this.waitFor(previous, waitMs)
    } catch (error) {
      release()
      throw error
    }
    try {
      return await fn()
    } finally {
      release()
    }
  }

  private async waitFor(previous: Promise<void>, waitMs: number): Promise<void> {
    if (waitMs <= 0) {
      await previous
      return
    }
    let timer: NodeJS.Timeout | undefined
    try {
      await Promise.race([
        previous,
        new Promise<never>((_, reject) => {
          timer = setTimeout(() => reject(new WorkerError("busy", "profile is busy")), waitMs)
        }),
      ])
    } finally {
      if (timer) clearTimeout(timer)
    }
  }

  private async withSlot<T>(fn: () => Promise<T>): Promise<T> {
    await this.acquireSlot()
    try {
      return await fn()
    } finally {
      this.releaseSlot()
    }
  }

  private acquireSlot(): Promise<void> {
    if (this.active < this.config.maxInFlight) {
      this.active++
      return Promise.resolve()
    }
    return new Promise((resolve) => {
      this.waiting.push(() => {
        this.active++
        resolve()
      })
    })
  }

  private releaseSlot(): void {
    this.active--
    const next = this.waiting.shift()
    if (next) next()
  }
}

function openPages(state: ProfileState): Page[] {
  return state.context.pages().filter((page) => !page.isClosed())
}

function isStorageState(value: unknown): value is StorageState {
  if (typeof value !== "object" || value === null) return false
  const candidate = value as { cookies?: unknown; origins?: unknown }
  return Array.isArray(candidate.cookies) && Array.isArray(candidate.origins)
}
