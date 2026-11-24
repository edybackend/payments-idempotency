// PostToolUse hook: formats the Go file the agent just changed, so style never drifts.
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'

const input = JSON.parse(readFileSync(0, 'utf8'))
const filePath = input.tool_input?.file_path ?? ''

try {
  if (filePath.endsWith('.go')) {
    execFileSync('gofmt', ['-w', filePath], { stdio: ['ignore', 'ignore', 'pipe'] })
  }
} catch (error) {
  // Formatting problems are reported but never block the edit; the Stop hook enforces the gates.
  console.error(`Formatter failed for ${filePath}: ${error.stderr?.toString() ?? error.message}`)
}
