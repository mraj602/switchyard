// node --test webui/static/*.test.cjs
// The labels of alert reasons on the board (components/alerts-fmt.js, push.md 3).

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const mod = (f) => {
  const src = fs.readFileSync(path.join(__dirname, f), 'utf8');
  if (/^import /m.test(src)) throw new Error(`${f} has imports; test only pure modules`);
  const names = [...src.matchAll(/^export (?:async )?(?:function|const|let|class) ([A-Za-z0-9_]+)/gm)].map((m) => m[1]);
  const body = src.replace(/^export /gm, '');
  return vm.runInNewContext(`(function () {\n${body}\nreturn { ${names.join(', ')} };\n})()`, { String });
};

const { reasonText, REASONS } = mod('components/alerts-fmt.js');

test('peers has its own label', () => {
  assert.equal(reasonText('peers'), 'talking to another session (peers)');
});

test('every known reason has a label, an unknown one reads as spaced words, none as empty', () => {
  for (const r of ['remote_control', 'rule_off', 'stale', 'superseded', 'baseline', 'answered', 'peers', 'ended', 'recovered', 'reset_passed', 'seen']) {
    assert.ok(REASONS[r], r);
    assert.equal(reasonText(r), REASONS[r]);
  }
  assert.equal(reasonText('some_new_reason'), 'some new reason');
  assert.equal(reasonText(null), '');
  assert.equal(reasonText(''), '');
});
