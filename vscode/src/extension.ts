// gil VS Code extension entry point.
//
// Pattern lifted from cline/src/extension.ts (slimmed):
//   - activate() resolves the gild socket and registers commands + a webview
//     view provider for the "gilSessions" sidebar view.
//   - No React, no Anthropic SDK; the panel is a single vanilla HTML file
//     loaded from disk and wired up via the webview message-passing API.
//   - All gild interaction goes through GildClient (gRPC over UDS).

import * as vscode from "vscode"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"
import { GildClient, SessionSummary } from "./gild_client"

let outputChannel: vscode.OutputChannel | undefined
let client: GildClient | undefined

function log(line: string): void {
	outputChannel?.appendLine(`[${new Date().toISOString()}] ${line}`)
}

function resolveSocketPath(): string {
	const cfg = vscode.workspace.getConfiguration("gil").get<string>("socketPath")
	if (cfg && cfg.trim().length > 0) {
		return cfg
	}
	const env = process.env["GILD_SOCKET"]
	if (env && env.length > 0) {
		return env
	}
	return path.join(os.homedir(), ".gil", "gild.sock")
}

function ensureClient(extensionDir: string): GildClient {
	if (client) {
		return client
	}
	const sock = resolveSocketPath()
	if (!fs.existsSync(sock)) {
		void vscode.window.showWarningMessage(
			`gil: gild socket not found at ${sock}. Start gild before invoking gil commands.`,
		)
	}
	const c = new GildClient(sock, extensionDir)
	c.connect()
	client = c
	log(`connected to gild at ${sock}`)
	return c
}

async function pickSession(c: GildClient): Promise<SessionSummary | undefined> {
	const sessions = await c.listSessions()
	if (sessions.length === 0) {
		void vscode.window.showInformationMessage("gil: no sessions found.")
		return undefined
	}
	const items: (vscode.QuickPickItem & { session: SessionSummary })[] = sessions.map((s) => ({
		label: s.id,
		description: s.status,
		detail: `${s.workingDir}${s.goalHint ? ` — ${s.goalHint}` : ""}`,
		session: s,
	}))
	const picked = await vscode.window.showQuickPick(items, {
		placeHolder: "Select a gil session",
		matchOnDescription: true,
		matchOnDetail: true,
	})
	return picked?.session
}

class GilSessionsViewProvider implements vscode.WebviewViewProvider {
	public static readonly viewType = "gilSessions"

	private view: vscode.WebviewView | undefined
	private activeTail: { sessionId: string; cancel: () => void } | undefined

	constructor(private readonly extensionUri: vscode.Uri) {}

	resolveWebviewView(
		webviewView: vscode.WebviewView,
		_context: vscode.WebviewViewResolveContext,
		_token: vscode.CancellationToken,
	): void {
		this.view = webviewView
		webviewView.webview.options = {
			enableScripts: true,
			localResourceRoots: [this.extensionUri],
		}
		webviewView.webview.html = this.renderHtml(webviewView.webview)
		webviewView.webview.onDidReceiveMessage((msg) => {
			void this.handleMessage(msg)
		})
		webviewView.onDidDispose(() => {
			this.activeTail?.cancel()
			this.activeTail = undefined
			this.view = undefined
		})

		// Prime the panel with the current session list.
		void this.refreshSessions()
	}

	private renderHtml(webview: vscode.Webview): string {
		const htmlPath = path.join(this.extensionUri.fsPath, "dist", "webview.html")
		try {
			let html = fs.readFileSync(htmlPath, "utf-8")
			html = html.replace(/\$\{cspSource\}/g, webview.cspSource)
			return html
		} catch (err) {
			return `<!doctype html><html><body><pre>Failed to load webview.html from ${htmlPath}: ${String(err)}</pre></body></html>`
		}
	}

	private post(message: unknown): void {
		void this.view?.webview.postMessage(message)
	}

	private async handleMessage(msg: any): Promise<void> {
		try {
			switch (msg?.type) {
				case "refresh":
					await this.refreshSessions()
					return
				case "tail":
					await this.startTail(String(msg.sessionId ?? ""))
					return
				case "stopTail":
					this.stopTail()
					return
				case "run":
					await vscode.commands.executeCommand("gil.runSession", String(msg.sessionId ?? ""))
					await this.refreshSessions()
					return
				case "createSession":
					await vscode.commands.executeCommand("gil.startSession")
					await this.refreshSessions()
					return
				default:
					log(`webview: ignoring unknown message type ${String(msg?.type)}`)
			}
		} catch (err) {
			log(`webview: handler error: ${String(err)}`)
			this.post({ type: "error", message: String(err) })
		}
	}

	async refreshSessions(): Promise<void> {
		try {
			const c = ensureClient(this.extensionUri.fsPath)
			const sessions = await c.listSessions()
			this.post({ type: "sessions", sessions })
		} catch (err) {
			this.post({ type: "error", message: `list sessions failed: ${String(err)}` })
		}
	}

	private async startTail(sessionId: string): Promise<void> {
		if (!sessionId) return
		this.stopTail()
		const c = ensureClient(this.extensionUri.fsPath)
		const stream = c.subscribeEvents(sessionId)
		this.activeTail = { sessionId, cancel: () => stream.cancel() }
		this.post({ type: "tailStarted", sessionId })
		try {
			for await (const ev of stream) {
				this.post({ type: "event", sessionId, event: ev })
			}
			this.post({ type: "tailEnded", sessionId })
		} catch (err) {
			this.post({ type: "error", message: `tail failed: ${String(err)}` })
		} finally {
			if (this.activeTail?.sessionId === sessionId) {
				this.activeTail = undefined
			}
		}
	}

	private stopTail(): void {
		if (this.activeTail) {
			this.activeTail.cancel()
			this.activeTail = undefined
		}
	}
}

export async function activate(context: vscode.ExtensionContext): Promise<void> {
	outputChannel = vscode.window.createOutputChannel("gil")
	context.subscriptions.push(outputChannel)
	log("activating gil extension")

	const provider = new GilSessionsViewProvider(context.extensionUri)
	context.subscriptions.push(
		vscode.window.registerWebviewViewProvider(GilSessionsViewProvider.viewType, provider, {
			webviewOptions: { retainContextWhenHidden: true },
		}),
	)

	context.subscriptions.push(
		vscode.commands.registerCommand("gil.startSession", async () => {
			try {
				const c = ensureClient(context.extensionUri.fsPath)
				const defaultWd = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? process.cwd()
				const workingDir = await vscode.window.showInputBox({
					prompt: "gil: working directory for the new session",
					value: defaultWd,
					ignoreFocusOut: true,
				})
				if (!workingDir) return
				const goalHint = await vscode.window.showInputBox({
					prompt: "gil: optional goal hint (free text, may be empty)",
					value: "",
					ignoreFocusOut: true,
				})
				const session = await c.createSession({
					workingDir,
					goalHint: goalHint ?? "",
				})
				log(`created session ${session.id} (${session.status}) in ${session.workingDir}`)
				void vscode.window.showInformationMessage(`gil: created session ${session.id}`)
				await provider.refreshSessions()
			} catch (err) {
				void vscode.window.showErrorMessage(`gil.startSession failed: ${String(err)}`)
			}
		}),
	)

	context.subscriptions.push(
		vscode.commands.registerCommand("gil.runSession", async (sessionIdArg?: string) => {
			try {
				const c = ensureClient(context.extensionUri.fsPath)
				let sessionId = sessionIdArg
				if (!sessionId) {
					const picked = await pickSession(c)
					if (!picked) return
					sessionId = picked.id
				}
				log(`run session ${sessionId} (detached)`)
				const result = await c.runSession({ sessionId, detach: true })
				void vscode.window.showInformationMessage(
					`gil: run ${result.status} (iterations=${result.iterations}, tokens=${result.tokens})`,
				)
				await provider.refreshSessions()
			} catch (err) {
				void vscode.window.showErrorMessage(`gil.runSession failed: ${String(err)}`)
			}
		}),
	)

	context.subscriptions.push(
		vscode.commands.registerCommand("gil.tailEvents", async (sessionIdArg?: string) => {
			try {
				const c = ensureClient(context.extensionUri.fsPath)
				let sessionId = sessionIdArg
				if (!sessionId) {
					const picked = await pickSession(c)
					if (!picked) return
					sessionId = picked.id
				}
				outputChannel?.show(true)
				log(`--- tail start: ${sessionId} ---`)
				const stream = c.subscribeEvents(sessionId)
				// Run the stream loop without blocking command completion.
				void (async () => {
					try {
						for await (const ev of stream) {
							const ts = new Date(ev.timestampMs).toISOString()
							log(`[${sessionId}] #${ev.id} ${ts} ${ev.source}/${ev.kind} ${ev.type} ${ev.dataJson}`)
						}
						log(`--- tail end: ${sessionId} ---`)
					} catch (err) {
						log(`--- tail error (${sessionId}): ${String(err)} ---`)
					}
				})()
			} catch (err) {
				void vscode.window.showErrorMessage(`gil.tailEvents failed: ${String(err)}`)
			}
		}),
	)

	context.subscriptions.push(
		vscode.commands.registerCommand("gil.openPanel", async () => {
			await vscode.commands.executeCommand("workbench.view.extension.gil-sidebar")
			await vscode.commands.executeCommand(`${GilSessionsViewProvider.viewType}.focus`)
		}),
	)
}

export function deactivate(): void {
	try {
		client?.dispose()
	} catch {
		// best-effort
	}
	client = undefined
	outputChannel?.dispose()
	outputChannel = undefined
}
