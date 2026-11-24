import { useCallback, useEffect, useState } from 'react'
import {
  type Attempt,
  type Chaos,
  type Payment,
  type PaymentRequest,
  type Stats,
  getChaos,
  getPayments,
  getStats,
  pay,
  setChaos,
} from './api.ts'

const cards = [
  { number: '4242 4242 4242 4242', label: '4242 · approves' },
  { number: '4000 0000 0000 0002', label: '0002 · declined' },
]

const newKey = () => crypto.randomUUID()

const money = (amount: number, currency: string) =>
  new Intl.NumberFormat('en', { style: 'currency', currency }).format(amount / 100)

export function App() {
  const [request, setRequest] = useState<PaymentRequest>({
    amount: 1990,
    currency: 'BRL',
    card: cards[0].number,
    description: 'Concert ticket',
  })
  const [key, setKey] = useState(newKey)
  const [attempts, setAttempts] = useState<Attempt[]>([])
  const [stats, setStats] = useState<Stats>()
  const [payments, setPayments] = useState<Payment[]>([])
  const [chaos, setChaosState] = useState<Chaos>({ latencyMs: 400, failNextCalls: 0, crashAfterCharge: 0 })
  const [busy, setBusy] = useState(false)

  // After each action: counters, payments, and the failure injection that the calls may have consumed.
  const refresh = useCallback(async () => {
    const [s, p, c] = await Promise.all([getStats(), getPayments(), getChaos()])
    setStats(s)
    setPayments(p)
    setChaosState(c)
  }, [])

  // Initial load from the API; the flag ignores a response that arrives after unmount.
  useEffect(() => {
    let active = true
    Promise.all([getStats(), getPayments(), getChaos()]).then(([s, p, c]) => {
      if (active) {
        setStats(s)
        setPayments(p)
        setChaosState(c)
      }
    })
    return () => {
      active = false
    }
  }, [])

  async function run(label: string, sends: Array<{ key: string; body: PaymentRequest }>) {
    setBusy(true)
    try {
      const results = await Promise.all(sends.map((s) => pay(label, s.key, s.body)))
      setAttempts((previous) => [...results.reverse(), ...previous].slice(0, 40))
      await refresh()
    } finally {
      setBusy(false)
    }
  }

  const same = { key, body: request }

  return (
    <main>
      <header>
        <h1>Payments · Idempotency keys</h1>
        <p>
          A retry with the same <code>Idempotency-Key</code> never charges twice. Click the buttons and watch the
          counters: <strong>payments</strong> and <strong>acquirer charges</strong> must always match.
        </p>
      </header>

      <section className="tiles">
        <Tile label="Payments" value={stats?.payments} />
        <Tile label="Acquirer charges" value={stats?.acquirerCharges} />
        <Tile label="Idempotency keys" value={stats?.keys} />
        <Tile label="Replayed responses" value={stats?.replays} />
      </section>

      <section className="grid">
        <form className="card" onSubmit={(e) => e.preventDefault()}>
          <h2>Request</h2>
          <label>
            Amount (minor units)
            <input
              type="number"
              value={request.amount}
              onChange={(e) => setRequest({ ...request, amount: Number(e.target.value) })}
            />
          </label>
          <label>
            Currency
            <select value={request.currency} onChange={(e) => setRequest({ ...request, currency: e.target.value })}>
              <option>BRL</option>
              <option>USD</option>
              <option>EUR</option>
            </select>
          </label>
          <label>
            Test card
            <select value={request.card} onChange={(e) => setRequest({ ...request, card: e.target.value })}>
              {cards.map((c) => (
                <option key={c.number} value={c.number}>
                  {c.label}
                </option>
              ))}
            </select>
          </label>
          <label>
            Idempotency-Key
            <span className="key">
              <code>{key}</code>
              <button type="button" onClick={() => setKey(newKey())}>
                New key
              </button>
            </span>
          </label>
          <div className="actions">
            <button disabled={busy} onClick={() => run('Pay', [same])}>
              Pay
            </button>
            <button disabled={busy} onClick={() => run('Retry same request', [same])}>
              Retry same request
            </button>
            <button disabled={busy} onClick={() => run('Double click ×5', Array.from({ length: 5 }, () => same))}>
              Double click ×5
            </button>
            <button
              disabled={busy}
              onClick={() => run('Same key, other amount', [{ key, body: { ...request, amount: request.amount + 100 } }])}
            >
              Same key, other amount
            </button>
          </div>
        </form>

        <form className="card" onSubmit={(e) => e.preventDefault()}>
          <h2>Acquirer failures</h2>
          <label>
            Latency (ms)
            <input
              type="number"
              value={chaos.latencyMs}
              onChange={(e) => setChaosState({ ...chaos, latencyMs: Number(e.target.value) })}
            />
          </label>
          <label>
            Fail the next calls (connection refused)
            <input
              type="number"
              value={chaos.failNextCalls}
              onChange={(e) => setChaosState({ ...chaos, failNextCalls: Number(e.target.value) })}
            />
          </label>
          <label>
            Charge, then lose the answer
            <input
              type="number"
              value={chaos.crashAfterCharge}
              onChange={(e) => setChaosState({ ...chaos, crashAfterCharge: Number(e.target.value) })}
            />
          </label>
          <div className="actions">
            <button onClick={async () => setChaosState(await setChaos(chaos))}>Apply</button>
          </div>
          <p className="hint">
            Try <em>charge, then lose the answer = 1</em>, then <strong>Pay</strong> (502) and{' '}
            <strong>Retry same request</strong> (201): one charge, one payment.
          </p>
        </form>
      </section>

      <section className="card">
        <h2>Requests</h2>
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>Action</th>
              <th>Key</th>
              <th>Status</th>
              <th>Replayed</th>
              <th>ms</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
            {attempts.map((a) => (
              <tr key={a.id}>
                <td>{a.id}</td>
                <td>{a.label}</td>
                <td>
                  <code>{a.key.slice(0, 8)}</code>
                </td>
                <td className={`status s${Math.floor(a.status / 100)}`}>{a.status}</td>
                <td>{a.replayed ? 'yes' : ''}</td>
                <td>{a.ms}</td>
                <td>{a.summary}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section className="card">
        <h2>Payments</h2>
        <table>
          <thead>
            <tr>
              <th>Id</th>
              <th>Amount</th>
              <th>Card</th>
              <th>Status</th>
              <th>Code</th>
            </tr>
          </thead>
          <tbody>
            {payments.map((p) => (
              <tr key={p.id}>
                <td>
                  <code>{p.id}</code>
                </td>
                <td>{money(p.amount, p.currency)}</td>
                <td>•••• {p.cardLast4}</td>
                <td>{p.status}</td>
                <td>{p.code}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </main>
  )
}

function Tile({ label, value }: { label: string; value?: number }) {
  return (
    <div className="tile">
      <span>{label}</span>
      <strong>{value ?? '–'}</strong>
    </div>
  )
}
