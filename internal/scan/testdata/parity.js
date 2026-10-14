// Runs the prototype's parseLine (core/transcripts.js) over the given transcript files and
// prints the merged result as JSON, for internal/scan's parity test. Rows are merged the way
// core/db.js tokenUpsert does: first line's metadata, largest value of each counter.
// Usage: node parity.js file.jsonl ...
const fs = require('node:fs');
const path = require('node:path');
const { parseLine } = require(path.join(__dirname, '..', '..', '..', 'core', 'transcripts.js'));
const { costOf, modelLabel } = require(path.join(__dirname, '..', '..', '..', 'core', 'pricing.js'));

const rows = new Map(), hits = new Map(), owners = new Map();
for (const f of process.argv.slice(2)) {
  for (const l of fs.readFileSync(f, 'utf8').split('\n')) {
    const r = parseLine(l);
    if (!r) continue;
    if (r.tok) {
      const [key, ts, session_id, cwd, model, input, output, cache_5m, cache_1h, cache_read, thinking, side] = r.tok;
      const o = rows.get(key);
      if (!o) rows.set(key, { key, ts, session_id: session_id || '', cwd: cwd || '', model: model || '', input, output, cache_5m, cache_1h, cache_read, thinking, sidechain: !!side });
      else for (const [k, v] of Object.entries({ input, output, cache_5m, cache_1h, cache_read, thinking })) o[k] = Math.max(o[k], v);
    } else if (r.hit) {
      const [session_id, ts, type, status, resets_at] = r.hit;
      const k = session_id + '|' + ts;
      if (!hits.has(k)) hits.set(k, { session_id, ts, type, status: status || '', resets_at: resets_at || 0 });
    } else if (r.owner) {
      if (!owners.has(r.owner[0])) owners.set(r.owner[0], { session_id: r.owner[0], owner_account: r.owner[1] || '', owner_org: r.owner[2] || '' });
    }
  }
}
const models = ['claude-opus-5-5', 'claude-opus-4-1-20250805', 'claude-sonnet-5', 'claude-sonnet-4-5-20250929', 'claude-haiku-4-5-20251001',
  'claude-fable-5-1', 'claude-3-5-haiku-20241022', 'claude-unknown-1', '<synthetic>', ''];
const t = { input: 1234567, output: 76543, cache5m: 345678, cache1h: 23456, cacheRead: 9876543 };
const pricing = models.map((m) => ({ model: m, cost: costOf(m, t), label: modelLabel(m) }));
process.stdout.write(JSON.stringify({ rows: [...rows.values()], hits: [...hits.values()], owners: [...owners.values()], pricing }));
