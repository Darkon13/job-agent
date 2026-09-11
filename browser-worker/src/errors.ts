export type WorkerErrorCode =
  | "invalid"
  | "unauthorized"
  | "not_found"
  | "busy"
  | "timeout"
  | "unsupported"
  | "internal"

const STATUS: Record<WorkerErrorCode, number> = {
  invalid: 400,
  unauthorized: 401,
  not_found: 404,
  busy: 409,
  timeout: 504,
  unsupported: 400,
  internal: 500,
}

export class WorkerError extends Error {
  readonly code: WorkerErrorCode

  constructor(code: WorkerErrorCode, message: string) {
    super(message)
    this.code = code
    this.name = "WorkerError"
  }

  get status(): number {
    return STATUS[this.code]
  }
}

export function toWorkerError(error: unknown): WorkerError {
  if (error instanceof WorkerError) return error
  if (error instanceof Error) {
    const message = error.message ?? ""
    if (error.name === "TimeoutError" || /timed? ?out/i.test(message)) {
      return new WorkerError("timeout", message)
    }
    return new WorkerError("internal", message)
  }
  return new WorkerError("internal", String(error))
}
