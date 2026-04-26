// Bundles src/extension.ts → dist/extension.js for the gil VS Code extension.
// Pattern lifted from cline/esbuild.mjs (slimmed: no React, no WASM, no aliases).
import * as esbuild from "esbuild"
import fs from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

const production = process.argv.includes("--production")
const watch = process.argv.includes("--watch")

const problemMatcherPlugin = {
	name: "problem-matcher",
	setup(build) {
		build.onStart(() => {
			console.log("[gil-vscode] build started")
		})
		build.onEnd((result) => {
			for (const { text, location } of result.errors) {
				console.error(`[gil-vscode] ERROR: ${text}`)
				if (location) {
					console.error(`    ${location.file}:${location.line}:${location.column}`)
				}
			}
			console.log(`[gil-vscode] build finished (${result.errors.length} errors)`)
		})
	},
}

// Copy webview.html into dist so the extension can read it at runtime.
const copyWebviewAssets = {
	name: "copy-webview-assets",
	setup(build) {
		build.onEnd(() => {
			const src = path.join(__dirname, "src", "webview.html")
			const dst = path.join(__dirname, "dist", "webview.html")
			if (fs.existsSync(src)) {
				fs.mkdirSync(path.dirname(dst), { recursive: true })
				fs.copyFileSync(src, dst)
			}
		})
	},
}

const extensionConfig = {
	entryPoints: ["src/extension.ts"],
	outfile: "dist/extension.js",
	bundle: true,
	platform: "node",
	format: "cjs",
	target: "node20",
	external: ["vscode"],
	sourcemap: !production,
	minify: production,
	logLevel: "silent",
	tsconfig: path.resolve(__dirname, "tsconfig.json"),
	plugins: [copyWebviewAssets, problemMatcherPlugin],
}

async function main() {
	const ctx = await esbuild.context(extensionConfig)
	if (watch) {
		await ctx.watch()
	} else {
		await ctx.rebuild()
		await ctx.dispose()
	}
}

main().catch((err) => {
	console.error(err)
	process.exit(1)
})
