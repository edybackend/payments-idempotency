// Stop hook: the agent cannot finish while vet, the race tests or the panel build are red. Mirrors CI.
import { execSync } from 'node:child_process'
import { readFileSync } from 'node:fs'

const input = JSON.parse(readFileSync(0, 'utf8'))
if (input.stop_hook_active) process.exit(0) // already blocked once: avoid an endless loop

const root = process.env.CLAUDE_PROJECT_DIR ?? process.cwd()
const sh = (command, cwd = root) => execSync(command, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })

const changed = sh('git status --porcelain').split('\n').filter(Boolean).map((line) => line.slice(3))
const gates = []
if (changed.some((file) => file.endsWith('.go') || file.startsWith('go.'))) {
  gates.push(['go vet ./...', root], ['go test -race ./...', root])
}
if (changed.some((file) => file.startsWith('web/'))) {
  gates.push(['npm run lint', `${root}/web`], ['npm run build', `${root}/web`])
}

for (const [command, cwd] of gates) {
  try {
    sh(command, cwd)
  } catch (error) {
    const output = `${error.stdout ?? ''}${error.stderr ?? ''}`.split('\n').slice(-40).join('\n')
    console.error(`Quality gate failed: \`${command}\`. Fix it before finishing.\n${output}`)
    process.exit(2)
  }
}
