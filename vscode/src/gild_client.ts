// Thin wrapper around @grpc/grpc-js + @grpc/proto-loader that targets the
// gild daemon over a Unix domain socket. Loads the protos shipped under
// ../proto/gil/v1 (relative to the extension install dir).
//
// Slim by design: only the RPCs the panel actually uses. No streaming
// abstractions beyond a Node async iterable for events.

import * as grpc from "@grpc/grpc-js"
import * as protoLoader from "@grpc/proto-loader"
import * as path from "node:path"
import * as fs from "node:fs"

export interface SessionSummary {
	id: string
	status: string
	workingDir: string
	goalHint: string
	totalTokens: number
	totalCostUsd: number
}

export interface EventSummary {
	id: number
	timestampMs: number
	source: string
	kind: string
	type: string
	dataJson: string
}

export interface CreateSessionInput {
	workingDir: string
	goalHint: string
}

export interface RunSessionInput {
	sessionId: string
	provider?: string
	model?: string
	detach?: boolean
}

export interface RunResult {
	status: string
	iterations: number
	tokens: number
	costUsd: number
	errorMessage: string
}

const SESSION_STATUS_NAMES: Record<number, string> = {
	0: "UNSPECIFIED",
	1: "CREATED",
	2: "INTERVIEWING",
	3: "FROZEN",
	4: "RUNNING",
	5: "AUTO_PAUSED",
	6: "DONE",
	7: "STOPPED",
}

const EVENT_SOURCE_NAMES: Record<number, string> = {
	0: "UNSPECIFIED",
	1: "AGENT",
	2: "USER",
	3: "ENVIRONMENT",
	4: "SYSTEM",
}

const EVENT_KIND_NAMES: Record<number, string> = {
	0: "UNSPECIFIED",
	1: "ACTION",
	2: "OBSERVATION",
	3: "NOTE",
}

function statusName(s: unknown): string {
	if (typeof s === "string") return s
	if (typeof s === "number") return SESSION_STATUS_NAMES[s] ?? String(s)
	return "UNKNOWN"
}

function sourceName(s: unknown): string {
	if (typeof s === "string") return s
	if (typeof s === "number") return EVENT_SOURCE_NAMES[s] ?? String(s)
	return "UNKNOWN"
}

function kindName(s: unknown): string {
	if (typeof s === "string") return s
	if (typeof s === "number") return EVENT_KIND_NAMES[s] ?? String(s)
	return "UNKNOWN"
}

function findProtoDir(extensionDir: string): string {
	// Try a few candidate locations so the extension works whether it is
	// installed via a packaged .vsix (protos copied alongside dist/) or run
	// from a source checkout (protos under ../proto/gil/v1).
	const candidates = [
		path.join(extensionDir, "proto", "gil", "v1"),
		path.join(extensionDir, "..", "proto", "gil", "v1"),
		path.join(extensionDir, "..", "..", "proto", "gil", "v1"),
	]
	for (const c of candidates) {
		if (fs.existsSync(path.join(c, "session.proto"))) {
			return c
		}
	}
	throw new Error(
		`gild_client: could not locate proto/gil/v1 (looked in: ${candidates.join(", ")})`,
	)
}

export class GildClient {
	private channel: grpc.Client | null = null
	private sessionService: any
	private runService: any

	constructor(
		private readonly socketPath: string,
		private readonly extensionDir: string,
	) {}

	connect(): void {
		const protoDir = findProtoDir(this.extensionDir)
		const protoFiles = [
			path.join(protoDir, "event.proto"),
			path.join(protoDir, "session.proto"),
			path.join(protoDir, "run.proto"),
			path.join(protoDir, "interview.proto"),
			path.join(protoDir, "spec.proto"),
		]
		const packageDef = protoLoader.loadSync(protoFiles, {
			keepCase: false,
			longs: Number,
			enums: Number,
			defaults: true,
			oneofs: true,
			includeDirs: [path.resolve(protoDir, "..", "..")],
		})
		const loaded = grpc.loadPackageDefinition(packageDef) as any
		const gilv1 = loaded?.gil?.v1
		if (!gilv1) {
			throw new Error("gild_client: failed to load gil.v1 package from protos")
		}

		const target = `unix://${this.socketPath}`
		const credentials = grpc.credentials.createInsecure()
		this.sessionService = new gilv1.SessionService(target, credentials)
		this.runService = new gilv1.RunService(target, credentials)
		this.channel = this.sessionService
	}

	dispose(): void {
		try {
			this.sessionService?.close?.()
			this.runService?.close?.()
		} catch {
			// best-effort
		}
		this.channel = null
	}

	async listSessions(limit = 50, statusFilter = ""): Promise<SessionSummary[]> {
		this.ensureConnected()
		return new Promise((resolve, reject) => {
			this.sessionService.List(
				{ limit, statusFilter: statusFilter },
				(err: Error | null, resp: any) => {
					if (err) {
						reject(err)
						return
					}
					const sessions: SessionSummary[] = (resp?.sessions ?? []).map((s: any) => ({
						id: s.id ?? "",
						status: statusName(s.status),
						workingDir: s.workingDir ?? "",
						goalHint: s.goalHint ?? "",
						totalTokens: Number(s.totalTokens ?? 0),
						totalCostUsd: Number(s.totalCostUsd ?? 0),
					}))
					resolve(sessions)
				},
			)
		})
	}

	async createSession(input: CreateSessionInput): Promise<SessionSummary> {
		this.ensureConnected()
		return new Promise((resolve, reject) => {
			this.sessionService.Create(
				{ workingDir: input.workingDir, goalHint: input.goalHint },
				(err: Error | null, s: any) => {
					if (err) {
						reject(err)
						return
					}
					resolve({
						id: s.id ?? "",
						status: statusName(s.status),
						workingDir: s.workingDir ?? "",
						goalHint: s.goalHint ?? "",
						totalTokens: Number(s.totalTokens ?? 0),
						totalCostUsd: Number(s.totalCostUsd ?? 0),
					})
				},
			)
		})
	}

	async runSession(input: RunSessionInput): Promise<RunResult> {
		this.ensureConnected()
		return new Promise((resolve, reject) => {
			this.runService.Start(
				{
					sessionId: input.sessionId,
					provider: input.provider ?? "",
					model: input.model ?? "",
					detach: input.detach ?? false,
				},
				(err: Error | null, resp: any) => {
					if (err) {
						reject(err)
						return
					}
					resolve({
						status: resp?.status ?? "",
						iterations: Number(resp?.iterations ?? 0),
						tokens: Number(resp?.tokens ?? 0),
						costUsd: Number(resp?.costUsd ?? 0),
						errorMessage: resp?.errorMessage ?? "",
					})
				},
			)
		})
	}

	// subscribeEvents returns an async iterable of events for a session.
	// The iterator ends when the server closes the stream or the caller
	// breaks out of the for-await loop (which triggers cancel()).
	subscribeEvents(sessionId: string): AsyncIterable<EventSummary> & { cancel: () => void } {
		this.ensureConnected()
		const call = this.runService.Tail({ sessionId })
		const buffer: EventSummary[] = []
		const waiters: Array<(v: IteratorResult<EventSummary>) => void> = []
		let ended = false
		let error: Error | null = null

		const push = (ev: EventSummary) => {
			const w = waiters.shift()
			if (w) {
				w({ value: ev, done: false })
			} else {
				buffer.push(ev)
			}
		}

		const finish = (err: Error | null) => {
			ended = true
			error = err
			while (waiters.length > 0) {
				const w = waiters.shift()!
				if (err) {
					// The async iterator throws via the for-await machinery.
					w({ value: undefined as unknown as EventSummary, done: true })
				} else {
					w({ value: undefined as unknown as EventSummary, done: true })
				}
			}
		}

		call.on("data", (raw: any) => {
			const tsSeconds = Number(raw?.timestamp?.seconds ?? 0)
			const tsNanos = Number(raw?.timestamp?.nanos ?? 0)
			push({
				id: Number(raw?.id ?? 0),
				timestampMs: tsSeconds * 1000 + Math.floor(tsNanos / 1_000_000),
				source: sourceName(raw?.source),
				kind: kindName(raw?.kind),
				type: raw?.type ?? "",
				dataJson: Buffer.isBuffer(raw?.dataJson) ? raw.dataJson.toString("utf-8") : String(raw?.dataJson ?? ""),
			})
		})
		call.on("end", () => finish(null))
		call.on("error", (err: Error) => finish(err))

		const iterable: AsyncIterable<EventSummary> & { cancel: () => void } = {
			[Symbol.asyncIterator](): AsyncIterator<EventSummary> {
				return {
					next(): Promise<IteratorResult<EventSummary>> {
						if (buffer.length > 0) {
							return Promise.resolve({ value: buffer.shift()!, done: false })
						}
						if (ended) {
							if (error) return Promise.reject(error)
							return Promise.resolve({ value: undefined as unknown as EventSummary, done: true })
						}
						return new Promise<IteratorResult<EventSummary>>((resolve) => {
							waiters.push(resolve)
						})
					},
					return(): Promise<IteratorResult<EventSummary>> {
						try {
							call.cancel()
						} catch {
							// ignore
						}
						finish(null)
						return Promise.resolve({ value: undefined as unknown as EventSummary, done: true })
					},
				}
			},
			cancel(): void {
				try {
					call.cancel()
				} catch {
					// ignore
				}
				finish(null)
			},
		}
		return iterable
	}

	private ensureConnected(): void {
		if (!this.channel) {
			throw new Error("gild_client: not connected (call connect() first)")
		}
	}
}
