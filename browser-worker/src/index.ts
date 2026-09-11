import { loadConfig } from "./config.js"
import { BrowserManager } from "./manager.js"
import { createWorkerServer } from "./server.js"

const config = loadConfig()
const manager = new BrowserManager(config)
const server = createWorkerServer(config, manager)

server.listen(config.port, config.host, () => {
  console.log(`browser-worker listening on ${config.host}:${config.port}`)
})

let shuttingDown = false

async function shutdown(signal: string): Promise<void> {
  if (shuttingDown) return
  shuttingDown = true
  console.log(`browser-worker shutting down after ${signal}`)
  server.close()
  await manager.shutdown()
  process.exit(0)
}

process.on("SIGINT", () => {
  void shutdown("SIGINT")
})
process.on("SIGTERM", () => {
  void shutdown("SIGTERM")
})
