import { test } from 'node:test';
import assert from 'node:assert/strict';

import * as urls from './urls.js';

test('claude.ai and its subdomains are claude URLs, https only', () => {
  assert.ok(urls.isClaudeUrl('https://claude.ai/new'));
  assert.ok(urls.isClaudeUrl('https://www.claude.ai/'));
  assert.ok(urls.isClaudeUrl('https://CLAUDE.AI/chat/x'));
  assert.ok(!urls.isClaudeUrl('http://claude.ai/'));
  assert.ok(!urls.isClaudeUrl('https://claude.ai.evil.com/'));
  assert.ok(!urls.isClaudeUrl('https://evilclaude.ai/'));
  assert.ok(!urls.isClaudeUrl('https://user:pw@claude.ai/'));
  assert.ok(!urls.isClaudeUrl('not a url'));
  assert.ok(!urls.isClaudeUrl(42));
});

test('a loopback test origin stands in for claude.ai only when given', () => {
  const o = urls.testOrigin('http://127.0.0.1:5555/whatever');
  assert.equal(o, 'http://127.0.0.1:5555');
  assert.equal(urls.testOrigin('http://example.com:5555'), null);
  assert.equal(urls.testOrigin('file:///etc/passwd'), null);
  assert.ok(urls.isClaudeUrl('http://127.0.0.1:5555/new', { claudeOrigin: o }));
  assert.ok(!urls.isClaudeUrl('http://127.0.0.1:5556/new', { claudeOrigin: o }));
  assert.ok(!urls.isClaudeUrl('http://127.0.0.1:5555/new'));
});

test('sign-in hops are anthropic.com, claude.com, challenges.cloudflare.com and subdomains, https only', () => {
  for (const u of ['https://anthropic.com/x', 'https://console.anthropic.com/', 'https://claude.com/login', 'https://challenges.cloudflare.com/cdn-cgi/x']) {
    assert.ok(urls.isSignInHop(u), u);
    assert.equal(urls.tabLinkAction(u), 'tab', u);
  }
  for (const u of ['http://anthropic.com/', 'https://cloudflare.com/', 'https://notanthropic.com/']) assert.ok(!urls.isSignInHop(u), u);
});

test('tab link actions: allowed hosts stay, https and mailto go out, the rest is dropped', () => {
  assert.equal(urls.tabLinkAction('https://claude.ai/settings'), 'tab');
  assert.equal(urls.tabLinkAction('https://example.com/'), 'external');
  assert.equal(urls.tabLinkAction('mailto:a@b.c'), 'external');
  for (const u of ['javascript:alert(1)', 'file:///etc/passwd', 'http://example.com/', 'data:text/html,x', 'app://shell/index.html', 'chrome://gpu', '']) {
    assert.equal(urls.tabLinkAction(u), 'deny', u);
  }
});

test('only https and mailto may reach the system browser', () => {
  assert.ok(urls.isExternalAllowed('https://example.com/a?b'));
  assert.ok(urls.isExternalAllowed('mailto:x@y.z'));
  for (const u of ['http://example.com', 'file:///x', 'javascript:1', 'smb://host/share', 'ms-settings:', 'https://u:p@x.com/', 'mailto:']) {
    assert.ok(!urls.isExternalAllowed(u), u);
  }
});

test('externalUrl: mailto keeps only to, subject, cc and body; https is unchanged', () => {
  assert.equal(urls.externalUrl('https://example.com/a?b=1#c'), 'https://example.com/a?b=1#c');
  assert.equal(urls.externalUrl('mailto:a@b.c'), 'mailto:a@b.c');
  assert.equal(
    urls.externalUrl('mailto:a@b.c?subject=Hi%20there&attach=/etc/passwd&body=x%26y&BCC=e@v.il&cc=c@d.e&from=me@x&Attachment=~/.ssh/id_rsa&to=t@u.v'),
    'mailto:a@b.c?subject=Hi%20there&body=x%26y&cc=c%40d.e&to=t%40u.v',
  );
  assert.equal(urls.externalUrl('mailto:a@b.c?attach=x'), 'mailto:a@b.c');
  assert.equal(urls.externalUrl('mailto:a@b.c?subject=%E0%A4%A&body=ok'), 'mailto:a@b.c?body=ok', 'a bad escape drops that field');
  for (const u of ['http://example.com', 'file:///x', 'mailto:', 'javascript:1']) assert.equal(urls.externalUrl(u), null, u);
});

test('hub link actions', () => {
  const hubOrigin = 'http://127.0.0.1:8787';
  assert.equal(urls.hubLinkAction('http://127.0.0.1:8787/#/sessions/x', { hubOrigin }), 'hub');
  assert.equal(urls.hubLinkAction('http://127.0.0.1:8788/', { hubOrigin }), 'deny');
  assert.equal(urls.hubLinkAction('https://claude.ai/code/session_01AbC', { hubOrigin }), 'session');
  assert.equal(urls.hubLinkAction('https://claude.ai/code/session_01AbC/x', { hubOrigin }), 'external');
  assert.equal(urls.hubLinkAction('https://github.com/x', { hubOrigin }), 'external');
  assert.equal(urls.hubLinkAction('mailto:x@y.z', { hubOrigin }), 'deny');
  assert.equal(urls.hubLinkAction('javascript:1', { hubOrigin }), 'deny');
  assert.equal(urls.hubLinkAction('http://127.0.0.1:8787/'), 'deny', 'no hub origin known: nothing is the hub');
});

test('readable pages: claude.ai, not /login or /logout', () => {
  assert.ok(urls.isReadablePage('https://claude.ai/new'));
  assert.ok(urls.isReadablePage('https://claude.ai/'));
  assert.ok(!urls.isReadablePage('https://claude.ai/login'));
  assert.ok(!urls.isReadablePage('https://claude.ai/login/'));
  assert.ok(!urls.isReadablePage('https://claude.ai/logout?x=1'));
  assert.ok(urls.isReadablePage('https://claude.ai/loginx'));
  assert.ok(!urls.isReadablePage('https://claude.com/new'));
});

test('loopback hosts, same origin, shell URLs', () => {
  for (const h of ['localhost', '127.0.0.1', '127.1.2.3', '[::1]', '::1']) assert.ok(urls.isLoopbackHost(h), h);
  for (const h of ['10.0.0.1', '127.example.com', 'localhost.evil.com']) assert.ok(!urls.isLoopbackHost(h), h);
  assert.ok(urls.sameOrigin('https://a.b:1/x', 'https://a.b:1'));
  assert.ok(!urls.sameOrigin('https://a.b:1/x', 'https://a.b:2'));
  assert.ok(!urls.sameOrigin('data:x', 'data:x'));
  assert.ok(urls.isShellUrl('app://shell/index.html'));
  assert.ok(!urls.isShellUrl('app://shellx/index.html'));
});

test('subframes of an account tab: the allow-list, https only, inert and blob: frames', () => {
  for (const u of ['https://claude.ai/', 'https://challenges.cloudflare.com/x', 'https://js.stripe.com/v3/', 'https://x.claudeusercontent.com/',
    'https://accounts.google.com/gsi/', 'about:blank', 'about:srcdoc', 'ABOUT:BLANK', 'blob:https://claude.ai/abc']) {
    assert.ok(urls.tabFrameAllowed(u), u);
  }
  for (const u of ['http://js.stripe.com/', 'https://stripe.com/', 'https://google.com/', 'about:config', 'data:text/html,x',
    'blob:null/abc', 'blob:https://evil.example/abc', 'https://u:p@js.stripe.com/', '', null, 42]) {
    assert.ok(!urls.tabFrameAllowed(u), String(u));
  }
  assert.ok(urls.tabFrameAllowed('http://127.0.0.1:5555/f', { claudeOrigin: 'http://127.0.0.1:5555' }));
  assert.ok(!urls.tabFrameAllowed('http://127.0.0.1:5556/f', { claudeOrigin: 'http://127.0.0.1:5555' }));
});

test('subframes of the hub view: its own origin only', () => {
  const o = { hubOrigin: 'https://hub.example:8443' };
  assert.ok(urls.hubFrameAllowed('https://hub.example:8443/x', o));
  assert.ok(urls.hubFrameAllowed('blob:https://hub.example:8443/abc', o));
  assert.ok(urls.hubFrameAllowed('about:srcdoc', o));
  assert.ok(!urls.hubFrameAllowed('https://hub.example/x', o));
  assert.ok(!urls.hubFrameAllowed('https://claude.ai/', o));
  assert.ok(!urls.hubFrameAllowed('https://hub.example:8443/x', {}));
});

test('hostForLog keeps the host only', () => {
  assert.equal(urls.hostForLog('https://evil.example:8443/a?token=x'), 'evil.example:8443');
  assert.equal(urls.hostForLog('data:text/html,secret'), 'data:');
  assert.equal(urls.hostForLog('nope'), '(not a URL)');
});
