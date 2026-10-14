// API-equivalent price estimate for Claude Code token usage, in USD per million tokens.
// Subscriptions are billed in % of a window, not dollars, so this is only a common yardstick
// for "how much work was this" across accounts and models. Source: Anthropic pricing page,
// cached 2026-10. Cache writes are 1.25x input (5-minute TTL) or 2x (1-hour TTL); the
// cache-read multiplier differs by model, so it is listed per row.
//
// Rows are matched by prefix against message.model, longest prefix first.
const RATES = [
  // prefix                 input  output  cacheRead
  ['claude-fable-5-1',       10,    50,     0.25],
  ['claude-mythos-5-1',      10,    50,     0.25],
  ['claude-fable-5',         10,    50,     1.00],
  ['claude-mythos-5',        10,    50,     1.00],
  ['claude-opus-5-5',        4,     20,     0.20],
  ['claude-opus-5',          5,     25,     0.50],
  ['claude-opus-4-8',        5,     25,     0.50],
  ['claude-opus-4-7',        5,     25,     0.50],
  ['claude-opus-4-6',        5,     25,     0.50],
  ['claude-opus-4-5',        5,     25,     0.50],
  ['claude-opus-4',          15,    75,     1.50], // 4.0 / 4.1
  ['claude-sonnet-5-5',      2,     10,     0.20],
  ['claude-sonnet-5',        2,     10,     0.20],
  ['claude-sonnet-4',        3,     15,     0.30], // 4.0 / 4.5 / 4.6
  ['claude-haiku-5-5',       0.10,  0.50,   0.01],
  ['claude-haiku-4-5',       1,     5,      0.10],
  ['claude-3-5-haiku',       0.80,  4,      0.08],
].sort((a, b) => b[0].length - a[0].length);

const FAMILY = [['fable', 'Fable'], ['mythos', 'Mythos'], ['opus', 'Opus'], ['sonnet', 'Sonnet'], ['haiku', 'Haiku']];

function rateFor(model) {
  const m = String(model || '').toLowerCase();
  const row = RATES.find(([p]) => m.startsWith(p));
  if (!row) return null;
  const [, input, output, read] = row;
  return { input, output, read, write5m: input * 1.25, write1h: input * 2 };
}

// t: {input, output, cache5m, cache1h, cacheRead}. Unknown models cost null (shown as "—").
function costOf(model, t) {
  const r = rateFor(model);
  if (!r) return null;
  return (t.input * r.input + t.output * r.output + t.cache5m * r.write5m + t.cache1h * r.write1h + t.cacheRead * r.read) / 1e6;
}

// "claude-opus-5-5" -> "Opus 5.5"; unknown ids are returned unchanged.
function modelLabel(model) {
  const m = String(model || '');
  const f = FAMILY.find(([k]) => m.includes(k));
  if (!f) return m || 'unknown';
  const ver = (m.split(f[0])[1] || '').replace(/^-/, '').replace(/-\d{8}$/, '').split('-').filter((x) => /^\d+$/.test(x)).join('.');
  return ver ? `${f[1]} ${ver}` : f[1];
}

module.exports = { rateFor, costOf, modelLabel, RATES };
