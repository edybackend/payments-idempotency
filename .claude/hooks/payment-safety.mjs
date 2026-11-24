// PreToolUse hook: two rules of a payment API that must never regress.
// 1. Every POST/PATCH route goes through the idempotency middleware (a retry must never charge twice).
// 2. A card number is never logged (only the last 4 digits are ever stored).
import { readFileSync } from 'node:fs'

const input = JSON.parse(readFileSync(0, 'utf8'))
const filePath = (input.tool_input?.file_path ?? '').replaceAll('\\', '/')
if (!filePath.endsWith('.go') || filePath.endsWith('_test.go')) process.exit(0)

const before = (() => {
  try {
    return readFileSync(filePath, 'utf8')
  } catch {
    return ''
  }
})()
const oldText = input.tool_input?.old_string
const after = oldText !== undefined ? before.replace(oldText, input.tool_input?.new_string ?? '') : (input.tool_input?.content ?? '')

const unwrapped = after
  .split('\n')
  .filter((line) => /mux\.Handle(Func)?\("(POST|PATCH) /.test(line) && !line.includes('Idempotency.Wrap('))
if (unwrapped.length > 0) {
  console.error(
    `Blocked: ${filePath} registers an unsafe route without the idempotency middleware:\n${unwrapped.join('\n')}\n` +
      'Wrap it: mux.Handle("POST /v1/...", d.Idempotency.Wrap(handler)).',
  )
  process.exit(2)
}

const logsCard = after
  .split('\n')
  .filter((line) => /(logger|slog|log)\.\w+\(/.test(line) && /\bCard\b|\.Card\b|"card"/.test(line))
if (logsCard.length > 0) {
  console.error(`Blocked: ${filePath} would log card data:\n${logsCard.join('\n')}\nLog the payment id or the last 4 digits only.`)
  process.exit(2)
}
