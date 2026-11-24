export type Payment = {
  id: string
  amount: number
  currency: string
  cardLast4: string
  description: string
  status: 'approved' | 'declined'
  code: string
  createdAt: string
}

export type Stats = { payments: number; acquirerCharges: number; keys: number; replays: number }

export type Chaos = { latencyMs: number; failNextCalls: number; crashAfterCharge: number }

export type PaymentRequest = { amount: number; currency: string; card: string; description: string }

/** What one POST /v1/payments returned, as shown in the log. */
export type Attempt = {
  id: number
  label: string
  key: string
  status: number
  replayed: boolean
  ms: number
  summary: string
}

let attemptId = 0

export async function pay(label: string, key: string, body: PaymentRequest): Promise<Attempt> {
  const started = performance.now()
  const res = await fetch('/v1/payments', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
    body: JSON.stringify(body),
  })
  const json = await res.json()
  const summary = res.ok
    ? `${json.id} · ${json.status} · ${json.code}`
    : json.errors
      ? `${json.title}: ${Object.keys(json.errors).join(', ')}`
      : `${json.title}`
  return {
    id: ++attemptId,
    label,
    key,
    status: res.status,
    replayed: res.headers.get('Idempotent-Replayed') === 'true',
    ms: Math.round(performance.now() - started),
    summary,
  }
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url)
  if (!res.ok) throw new Error(`${url}: ${res.status}`)
  return res.json() as Promise<T>
}

export const getStats = () => getJSON<Stats>('/v1/stats')
export const getPayments = () => getJSON<Payment[]>('/v1/payments')
export const getChaos = () => getJSON<Chaos>('/v1/chaos')

export async function setChaos(chaos: Chaos): Promise<Chaos> {
  const res = await fetch('/v1/chaos', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(chaos),
  })
  return res.json() as Promise<Chaos>
}
