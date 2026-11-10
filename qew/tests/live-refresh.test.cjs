const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const thresholds = require('../pkg/web/static/compaction-threshold.js');

const app = fs.readFileSync(process.env.QEW_APP_SOURCE ||
  path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');

test('message sends only queue accepted prompts and preserve rejected or newer drafts', async () => {
  for (const scenario of ['accepted', 'rejected', 'transport failure', 'new draft', 'other session']) {
    const input = { value: 'original prompt', style: {} };
    const alerts = [];
    let loads = 0;
    const context = vm.createContext({
      currentSession: 'ssrf', currentSessionStatus: 'idle', qewConnected: true,
      sendInFlight: false, pendingAttachments: [], chatInputCache: { ssrf: input.value },
      overrideModel: null, overrideEffort: null, overrideContext: null,
      queuedMessages: [], chatTotal: 10,
      document: { getElementById: () => input },
      setConnectionState() {}, newOperationID: () => 'operation',
      clearPendingAttachments() {}, alert: message => alerts.push(message),
      loadChat: async () => { loads++; },
      apiCall: async (verb, noun, args) => {
        assert.deepEqual([verb, noun, ...args],
          ['continue', 'session', 'ssrf', '--async', '--operation-id', 'operation', 'original prompt']);
        if (scenario === 'transport failure') throw new Error('HTTP 502');
        if (scenario === 'rejected') return { status: 'error', message: 'Session unavailable' };
        if (scenario === 'new draft') input.value = 'next prompt';
        if (scenario === 'other session') {
          context.currentSession = 'other';
          input.value = 'other draft';
        }
        return { status: 'ok', data: { accepted: true } };
      },
    });
    vm.runInContext(app.slice(app.indexOf('  async function sendSessionPrompt('),
      app.indexOf('  // --- Deploy Agent Wizard ---')), context);
    await context.sendMessage();
    const failed = scenario === 'rejected' || scenario === 'transport failure';
    const queued = !failed && scenario !== 'other session';
    assert.equal(context.queuedMessages.length, queued ? 1 : 0, scenario);
    assert.equal(loads, queued ? 1 : 0, scenario);
    assert.equal(input.value, scenario === 'accepted' ? '' :
      scenario === 'new draft' ? 'next prompt' : scenario === 'other session' ? 'other draft' : 'original prompt', scenario);
    assert.equal(context.sendInFlight, false, scenario);
    assert.deepEqual(alerts, failed ?
      ['Send error: ' + (scenario === 'rejected' ? 'Session unavailable' : 'HTTP 502')] : [], scenario);
  }
});

test('rejected review submissions retain comments and do not appear queued', async () => {
  const button = {};
  const alerts = [];
  const context = vm.createContext({
    currentSession: 'ssrf', diffReview: { comments: { 1: 'review comment' } },
    document: { getElementById: id => id === 'diff-send-submit' ? button : { value: 'overall' } },
    buildReviewPrompt: () => 'review prompt',
    apiCall: async () => ({ status: 'error', message: 'Session unavailable' }),
    localStorage: { removeItem: () => assert.fail('must retain review comments') },
    closeWizard: () => assert.fail('must keep review open'),
    queuedMessages: [], alert: message => alerts.push(message),
  });
  vm.runInContext(app.slice(app.indexOf('  async function sendSessionPrompt('),
    app.indexOf('  async function sendMessage(')), context);
  vm.runInContext(app.slice(app.indexOf('  async function submitReview('),
    app.indexOf('  async function showDiff(')), context);
  await context.submitReview();
  assert.deepEqual(alerts, ['Send error: Session unavailable']);
  assert.equal(context.queuedMessages.length, 0);
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, 'Send to agent');
});

test('session Back/Forward stays in Qew across nested sessions, reloads and deep links', async () => {
  for (const deepLink of [false, true]) {
    const entries = [
      { url: new URL('https://qew.test/login'), state: null },
      { url: new URL('https://qew.test/?filter=active' + (deepLink ? '#/session/parent' : '')), state: null },
    ];
    let index = 1;
    const elements = new Map();
    const context = vm.createContext({
      currentSession: null, currentSessionStatus: '', parentSessionStack: [], chatGeneration: 0,
      chatInputCache: {}, localStorage: { getItem: () => null },
      document: { getElementById(id) {
        if (!elements.has(id)) elements.set(id, { style: {}, value: '', focus() {} });
        return elements.get(id);
      } },
      releaseActiveWatch() {}, stopDashboardPoll() {}, stopChatPoll() {},
      syncComposeModeToggle() {}, sessionNameFromCache: id => id, sessionNickFromCache: () => '',
      syncSchedulesToggle() {}, setChatTitle() {}, clearPendingAttachments() {}, autoResize() {},
      loadChat: async () => {}, requestActiveWatch() {}, startChatPoll() {},
      loadDashboard() {}, startDashboardPoll() {},
      window: { addEventListener() {} },
    });
    const location = {};
    for (const key of ['hash', 'pathname', 'search']) {
      Object.defineProperty(location, key, { get: () => entries[index].url[key] });
    }
    context.window.location = location;
    function move(step) {
      const previous = location.hash;
      index += step;
      assert.ok(index >= 0 && index < entries.length);
      if (previous !== location.hash) context.handleRoute();
    }
    context.history = {
      get state() { return entries[index].state; },
      replaceState(state, unused, url) {
        entries[index] = { state, url: new URL(url, entries[index].url) };
      },
      pushState(state, unused, url) {
        const next = { state, url: new URL(url, entries[index].url) };
        entries.splice(index + 1);
        entries.push(next);
        index++;
      },
      back: () => move(-1), forward: () => move(1),
    };
    vm.runInContext(app.slice(app.indexOf('  async function openChat('), app.indexOf('  function loadChat(')), context);
    vm.runInContext(app.slice(app.indexOf('  function openDashEntry('), app.indexOf('  // Re-apply the keyboard selection')), context);
    vm.runInContext(app.slice(app.indexOf('  function initializeRouteHistory('),
      app.indexOf("  window.addEventListener('hashchange'")), context);
    context.initializeRouteHistory();
    if (deepLink) context.handleRoute();
    else await context.openChat('parent');
    assert.equal(context.currentSession, 'parent');
    assert.equal(entries[index - 1].url.hash, '');
    assert.equal(entries[index - 1].url.pathname, '/');
    context.document.getElementById('chat-input').value = 'parent draft';
    context.window._openSubagent('child', 'Child', 'windows');
    assert.equal(context.currentSessionMP, 'windows');
    context.document.getElementById('chat-input').value = 'child draft';
    context.history.back();
    assert.equal(context.currentSession, 'parent');
    assert.equal(context.document.getElementById('chat-input').value, 'parent draft');
    assert.equal(context.parentSessionStack.length, 0);
    context.history.forward();
    assert.equal(context.currentSession, 'child');
    assert.equal(context.document.getElementById('chat-input').value, 'child draft');
    assert.equal(context.parentSessionStack[0].id, 'parent');
    // Reload must reuse the existing entry rather than add another dashboard.
    const length = entries.length;
    context.initializeRouteHistory();
    assert.equal(entries.length, length);
    context.closeChat();
    assert.equal(context.currentSession, 'parent');
    context.closeChat();
    assert.equal(context.currentSession, null);
    assert.equal(location.pathname, '/');
    assert.equal(location.search, '?filter=active');
    assert.equal(location.hash, '');
    context.history.forward();
    assert.equal(context.currentSession, 'parent');
    context.closeChat();
    context.openDashEntry({ sessionId: 'child', name: 'Child', mp: 'windows', parent: 'parent' });
    assert.equal(context.currentSession, 'child');
    assert.equal(context.currentSessionMP, 'windows');
    assert.equal(context.parentSessionStack.length, 0);
    assert.equal(entries[index - 1].url.hash, '');
    context.closeChat();
    assert.equal(context.currentSession, null);
    assert.equal(location.hash, '');
    context.history.forward();
    assert.equal(context.currentSession, 'child');
    assert.equal(context.parentSessionStack.length, 0);
  }
});

test('Markdown preserves literal code and PowerShell continuation backticks', () => {
  const escapeHtml = text => text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
  const context = vm.createContext({ escapeHtml, safeLinkHref: url => url });
  vm.runInContext(app.slice(app.indexOf('  function formatContent('), app.indexOf('  // --- Auto-resize textarea ---')), context);
  const command = [
    'python .\\scripts\\analyze_all_recordings.py `',
    '  "C:\\Users\\charlesfeval\\OneDrive - Microsoft\\20-Current\\video-sharing\\quality-study\\runs\\2026-10-19" `',
    '  --fidelity `',
    '  --ignore-audio `',
    '  --fidelity-starts 40 160 280 400 520 `',
    '  --fidelity-duration 10',
  ].join('\n');
  const literal = '# header\n## nested\n**bold** _italic_ `tick`\n> quote\n| a | b |\n| - | - |\nhttps://example.com [link](https://example.com)\n<script>alert("x")</script>\n' + command;
  for (const fence of ['```powershell', '~~~~text', '````language-with-dashes']) {
    const delimiter = fence.match(/^[`~]+/)[0];
    assert.equal(context.formatContent(fence + '\n' + literal + '\n' + delimiter),
      '<pre><code>' + escapeHtml(literal) + '\n</code></pre>');
  }
  assert.equal(context.formatContent('```powershell\n' + command), '<pre><code>' + command + '</code></pre>');
  assert.equal(context.formatContent(command), command.replaceAll('\n', '<br>'));
  assert.equal(context.formatContent('`# header **literal** https://example.com <tag>`'),
    '<code># header **literal** https://example.com &lt;tag&gt;</code>');
  assert.equal(context.formatContent('``literal `backticks` here``'),
    '<code>literal `backticks` here</code>');
  assert.equal(context.formatContent('# Heading\n**bold** and `code`\nplain'),
    '<h1>Heading</h1><br><strong>bold</strong> and <code>code</code><br>plain');
  assert.equal(context.formatContent('```ps1\r\n# literal\r\n```'),
    '<pre><code># literal\r\n</code></pre>');
  assert.equal(context.formatContent('\0' + '0\0 `safe`'), '&#0;0&#0; <code>safe</code>');
});

test('conversation help retains action, compose, focus and scrolling shortcuts', () => {
  const source = app.slice(app.indexOf("  document.addEventListener('keydown', (e) => {"), app.indexOf('  // --- Routing ---'));
  const actions = {
    c: 'complete', u: 'markready', r: 'resolveattention', e: 'edit', y: 'duplicate', a: 'subagent',
    l: 'allsubagents', p: 'project', g: 'diff', t: 'thoughts', o: 'model',
    f: 'effort', w: 'context', m: 'memory', n: 'channels', h: 'schedules',
    K: 'compact', D: 'distill', s: 'stop', d: 'delete', q: 'back',
  };
  const cases = [
    ...Object.entries(actions).map(([key, action]) => [{ key }, [['command', action]], true]),
    [{ key: '1' }, [['close'], ['subagent', 'child', 'Child']], true],
    [{ key: 'Escape' }, [['close-palette', true]], true],
    [{ key: 'p', ctrlKey: true }, [['close-palette', false], ['compose']], true],
    [{ key: 'i' }, [['close-palette', false], ['focus-input', true]], true],
    ...['j', 'k'].map((key, i) => [{ key, ctrlKey: true, repeat: true }, [['nav'], ['line', i === 0 ? 1 : -1]], true]),
    ...['j', 'ArrowDown', 'k', 'ArrowUp'].map((key, i) => [{ key }, [['nav'], ['line', i < 2 ? 1 : -1]], true]),
    ...['d', 'u'].flatMap((key, i) => ['ctrlKey', 'metaKey'].map(modifier =>
      [{ key, [modifier]: true }, [['page', i === 0 ? 1 : -1]], true])),
    [{ key: 'e', repeat: true }, [], false],
    [{ key: 'p', ctrlKey: true, repeat: true }, [], false],
    [{ key: 'e', isComposing: true }, [], false],
    [{ key: 'e', altKey: true }, [], false],
  ];
  for (const [modal, inputs, expected, prevented] of [
    ...cases.map(([inputs, expected, prevented]) => ['help', inputs, expected, prevented]),
    ['none', { key: 'p', ctrlKey: true }, [['compose']], true],
    ['none', { key: 'i' }, [['focus-input', true]], true],
    ['none', { key: 'd', ctrlKey: true }, [['page', 1]], true],
    ...['p', 'j', 'k', 'd', 'u'].map(key => ['edit', { key, ctrlKey: true }, [], false]),
    ['edit', { key: 'i' }, [], false],
  ]) {
    const calls = [];
    let listener;
    const input = {};
    const context = vm.createContext({
      currentSession: 'parent', cmdPaletteOpen: modal === 'help', chatNavMode: false,
      overridePicker: null, lastSubagents: [{ sessionId: 'child', name: 'Child' }],
      document: {
        activeElement: {},
        getElementById: id => id === 'chat-input' ? input : { style: { display: 'flex' } },
        querySelector: selector => selector === '.modal-overlay' ? modal !== 'none' :
          selector === '.cmd-palette[aria-label="Session commands"]' ? modal === 'help' : null,
        addEventListener: (event, handler) => { listener = handler; },
      },
      closeCmdPalette: focus => calls.push(['close-palette', focus]),
      toggleComposeMode: () => calls.push(['compose']),
      exitChatNavMode: focus => calls.push(['focus-input', focus]),
      enterChatNavMode: () => calls.push(['nav']),
      chatScrollLine: direction => calls.push(['line', direction]),
      chatScroll: direction => calls.push(['page', direction]),
      runCmd: command => calls.push(['command', command]),
      closeWizard: () => calls.push(['close']),
      window: { _openSubagent: (...args) => calls.push(['subagent', ...args]) },
      handleProjectFilterKey: () => false,
      handleAllSubagentsKey: () => false, handleDiffModalKey: () => false, handleWizardListKey() {},
    });
    vm.runInContext(source, context);
    let defaultPrevented = false;
    listener({ ctrlKey: false, metaKey: false, altKey: false, repeat: false, isComposing: false,
      ...inputs, preventDefault() { defaultPrevented = true; } });
    assert.deepEqual(calls, expected, `${modal}: ${JSON.stringify(inputs)}`);
    assert.equal(defaultPrevented, prevented, `${modal}: ${JSON.stringify(inputs)}`);
  }
});

test('resolve attention command reuses the banner resolver after closing the palette', () => {
  const calls = [];
  const context = vm.createContext({
    cmdPaletteOpen: true,
    closeWizard: () => calls.push('close'),
    resolveAttention: () => calls.push('resolve'),
  });
  vm.runInContext(app.slice(app.indexOf('  function runCmd('),
    app.indexOf('  function subagentsFromResponse(')), context);
  context.runCmd('resolveattention');
  assert.equal(context.cmdPaletteOpen, false);
  assert.deepEqual(calls, ['close', 'resolve']);
});

test('Add subagent opens the shared create wizard with parent host and path', async () => {
  let step1 = 0;
  const context = vm.createContext({
    currentSession: 'parent', projectsCache: [], traitsCache: [],
    renderWizardModal() {}, escapeHtml: text => text,
    loadTraitsCache: async () => {},
    showWizardStep1: async () => { step1++; },
    apiCall: async (verb, noun, args) => {
      assert.deepEqual([verb, noun, ...args], ['show', 'session', 'parent']);
      return { status: 'ok', data: { moneypenny: 'parent-host', path: '/parent/path' } };
    },
  });
  vm.runInContext(app.slice(app.indexOf('  async function createNewSubagent('), app.indexOf('  async function adoptSession(')), context);
  await context.createNewSubagent();
  assert.equal(step1, 1);
  assert.equal(context.wizardState.parentId, 'parent');
  assert.equal(context.wizardState.selectedMP, 'parent-host');
  assert.equal(context.wizardState.currentPath, '/parent/path');
  assert.equal(context.wizardState.copy, false);
});

test('shared create wizard routes creation modes and retains fields on failure', async () => {
  for (const mode of ['create', 'copy', 'subagent', 'subagent-error']) {
    const values = {
      'wiz-prompt': 'Build it', 'wiz-name': 'worker', 'wiz-nick': 'worker-nick',
      'wiz-project': 'project', 'wiz-agent': 'copilot', 'wiz-model': 'model',
      'wiz-effort': 'high', 'wiz-context': 'long_context', 'wiz-sysprompt': 'instructions',
      'wiz-env': 'SETTING=value', 'wiz-compaction': 'custom', 'wiz-compaction-threshold': '150',
      'wiz-ponsonbies': mode === 'create' ? '' : mode === 'copy' ? 'engineering:read' : 'none',
    };
    const elements = Object.fromEntries(Object.entries(values).map(([id, value]) => [id, { value }]));
    elements['wiz-yolo'] = { checked: false };
    elements['wiz-gadgets'] = { checked: true };
    elements['wiz-submit'] = {};
    const subagent = mode.startsWith('subagent');
    const events = [];
    const context = vm.createContext({
      wizardState: { copy: mode === 'copy', sourceId: 'source', parentId: subagent ? 'parent' : '',
        selectedMP: 'host', currentPath: '/work' },
      document: {
        getElementById: id => elements[id],
        querySelectorAll: () => [{ value: 'trait' }],
      },
      traitsCache: [{ id: 'trait' }],
      gadgetCapabilities: { args: () => ['--gadget-memory=false'] },
      selectedPonsonbies: () => values['wiz-ponsonbies'],
      validateCompactionThreshold: () => '',
      window: { jamesCompactionThreshold: thresholds,
        _openSubagent: (...args) => events.push(['open', ...args]) },
      closeWizard: () => events.push(['close']),
      loadDashboard: async () => events.push(['dashboard']),
      loadChat: async () => events.push(['chat']),
      alert: message => events.push(['error', message]),
      apiCall: async (verb, noun, args) => {
        events.push([verb, noun, Array.from(args)]);
        return mode === 'subagent-error' ? { status: 'error', message: 'rejected' } :
          { status: 'ok', data: { session_id: 'child' } };
      },
    });
    vm.runInContext(app.slice(app.indexOf('  async function submitCreateSession('), app.indexOf('  function renderWizardModal(')), context);
    await context.submitCreateSession();
    const [verb, noun, args] = events[0];
    assert.equal(verb, mode === 'copy' ? 'copy' : 'create');
    assert.equal(noun, subagent ? 'subsession' : 'session');
    if (subagent) assert.equal(args[0], 'parent');
    if (mode === 'copy') assert.equal(args[0], 'source');
    for (const [flag, value] of [
      ['-m', 'host'], ['--path', '/work'], ['--name', 'worker'], ['--project', 'project'],
      ['--agent', 'copilot'], ['--model', 'model'], ['--effort', 'high'], ['--context', 'long_context'],
      ['--system-prompt', 'instructions'], ['--env', 'SETTING=value'],
      ['--compaction', 'custom'], ['--compaction-threshold-tokens', '150000'], ['--traits', 'trait'],
    ]) assert.equal(args[args.indexOf(flag) + 1], value, flag);
    assert.ok(args.includes('--gadget-memory=false'));
    assert.equal(args[args.indexOf('--ponsonbies') + 1], values['wiz-ponsonbies'] || 'none');
    if (subagent) assert.ok(args.includes('--yolo=false'));
    assert.equal(args.at(-1), 'Build it');
    if (mode === 'subagent-error') {
      assert.deepEqual(events.slice(1), [['error', 'Error: rejected']]);
      assert.equal(elements['wiz-submit'].disabled, false);
      assert.equal(elements['wiz-submit'].textContent, 'Deploy Subagent');
    } else {
      assert.deepEqual(events.slice(1), subagent ? [['close'], ['open', 'child', 'worker']] : [['close'], ['dashboard']]);
    }
  }
});

test('moneypenny setup commands verify downloads and preserve installations by default', () => {
  const context = vm.createContext({});
  vm.runInContext(app.slice(app.indexOf('  function moneypennySetupCommand('), app.indexOf('  async function showAddMoneypennyModal(')), context);
  const pin = 'SHA256:' + 'a'.repeat(43);
  const artifact = name => ({ url: 'https://github.com/cfe84/james/releases/download/v1.2.3/' + name, sha256: 'a'.repeat(64) });
  for (const platform of ['mac', 'windows']) {
    const release = artifact(platform === 'mac' ? 'james-darwin-arm64.tar.gz' : 'james-windows-amd64.zip');
    const command = context.moneypennySetupCommand(platform, release, 'relay:443/laptop', pin, false);
    assert.match(command, /--non-interactive --user --auto-update/);
    assert.match(command, /--show-public-key/);
    assert.doesNotMatch(command, /--force/);
    assert.ok(command.indexOf(platform === 'mac' ? 'shasum' : 'Get-FileHash') < command.indexOf('--show-public-key'));
    assert.match(context.moneypennySetupCommand(platform, release, 'relay:443/laptop', pin, true), /--force/);
    assert.throws(() => context.moneypennySetupCommand(platform, release, 'relay:443/', pin), /session ID/);
    assert.throws(() => context.moneypennySetupCommand(platform, release, 'relay:443/laptop', ''), /fingerprint/);
    assert.throws(() => context.moneypennySetupCommand(platform, { ...release, sha256: '' }, 'relay:443/laptop', pin), /metadata/);
  }
  const { spawnSync } = require('node:child_process');
  const command = context.moneypennySetupCommand('mac', artifact('james-darwin-arm64.tar.gz'), 'relay:443/laptop', pin);
  assert.equal(spawnSync('/bin/bash', ['-n'], { input: command }).status, 0);
});

test('mac setup executes only a verified archive and passes service arguments intact', () => {
  const { spawnSync } = require('node:child_process');
  const crypto = require('node:crypto');
  const os = require('node:os');
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'james-setup-test-'));
  try {
    const tools = path.join(root, 'tools');
    const bundle = path.join(root, 'james-darwin-arm64');
    fs.mkdirSync(tools);
    fs.mkdirSync(bundle);
    fs.writeFileSync(path.join(tools, 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Darwin;; -m) echo arm64;; esac\n', { mode: 0o755 });
    fs.writeFileSync(path.join(tools, 'curl'), '#!/bin/sh\nwhile [ "$#" -gt 0 ]; do if [ "$1" = -o ]; then cp "$FIXTURE_ARCHIVE" "$2"; exit; fi; shift; done\nexit 1\n', { mode: 0o755 });
    fs.writeFileSync(path.join(bundle, 'moneypenny'), '#!/bin/sh\nprintf "%s\\n" "$@" >> "$FIXTURE_CALLS"\nif [ "$1" = --show-public-key ]; then echo "ssh-ed25519 TEST_PUBLIC_KEY"; fi\n', { mode: 0o755 });
    const archive = path.join(root, 'fixture.tar.gz');
    const packed = spawnSync('tar', ['-czf', archive, '-C', root, 'james-darwin-arm64']);
    assert.equal(packed.status, 0, packed.stderr.toString());
    const context = vm.createContext({});
    vm.runInContext(app.slice(app.indexOf('  function moneypennySetupCommand('), app.indexOf('  async function showAddMoneypennyModal(')), context);
    const pin = 'SHA256:' + 'a'.repeat(43);
    const digest = crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex');
    for (const verified of [true, false]) {
      const home = path.join(root, verified ? 'verified home' : 'rejected home');
      fs.mkdirSync(home);
      const calls = path.join(home, 'calls');
      const command = context.moneypennySetupCommand('mac', {
        url: 'https://github.com/cfe84/james/releases/download/v1.2.3/james-darwin-arm64.tar.gz',
        sha256: verified ? digest : '0'.repeat(64),
      }, "relay'quoted:443/laptop", pin, false);
      const result = spawnSync('/bin/bash', ['-c', command], {
        env: { ...process.env, HOME: home, PATH: tools + path.delimiter + process.env.PATH, FIXTURE_ARCHIVE: archive, FIXTURE_CALLS: calls },
      });
      if (verified) {
        assert.equal(result.status, 0, result.stderr.toString());
        assert.ok(fs.existsSync(calls), result.stdout.toString() + result.stderr.toString());
        assert.equal(fs.readFileSync(calls, 'utf8'), [
          '--show-public-key', 'install', '--non-interactive', '--user', '--auto-update',
          '--mi6', "relay'quoted:443/laptop", '--mi6-server-fingerprint', pin, '--show-public-key', '',
        ].join('\n'));
        assert.match(result.stdout.toString(), /TEST_PUBLIC_KEY/);
      } else {
        assert.notEqual(result.status, 0);
        assert.equal(fs.existsSync(calls), false);
        assert.equal(fs.existsSync(path.join(home, '.local/share/james')), false);
      }
    }
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('moneypenny onboarding exposes partial outcomes and never rolls trust back', async () => {
  for (const scenario of ['success', 'no-key', 'local', 'trust-failed', 'registration-failed', 'connectivity-failed', 'private-key', 'prefill-failed']) {
    const elements = new Map();
    const element = id => {
      if (!elements.has(id)) elements.set(id, {
        value: '', style: {}, listeners: {},
        addEventListener(event, callback) { this.listeners[event] = callback; },
      });
      return elements.get(id);
    };
    const calls = [];
    const context = vm.createContext({
      document: { getElementById: element },
      renderWizardModal() {}, closeWizard() { throw new Error('Do not hide onboarding outcomes'); },
      loadMoneypennies() {}, alert(message) { throw new Error(message); },
      apiCall: async (verb, noun, args) => {
        calls.push([verb, noun, Array.from(args)]);
        if (verb === 'diagnose') return scenario === 'prefill-failed' ? { status: 'error', message: 'Unavailable' } : { status: 'ok', data: {} };
        const failure = scenario === 'trust-failed' && noun === 'mi6-key' ||
          ['registration-failed', 'connectivity-failed'].includes(scenario) && verb === 'add' && noun === 'moneypenny';
        return failure ? { status: 'error', message: scenario === 'connectivity-failed' ? 'connectivity check failed' : 'Rejected' } : { status: 'ok' };
      },
    });
    vm.runInContext(app.slice(app.indexOf('  async function showAddMoneypennyModal('), app.indexOf('  // --- Projects Management ---')), context);
    await context.showAddMoneypennyModal();
    if (scenario === 'prefill-failed') {
      assert.match(element('mp-onboarding-status').textContent, /Could not load relay defaults.*Unavailable/);
      continue;
    }
    element('mp-name').value = 'laptop';
    element('mp-type').value = scenario === 'local' ? 'local' : 'mi6';
    element('mp-mi6-addr').value = 'relay:443/laptop';
    element('mp-mi6-server-fingerprint').value = 'SHA256:' + 'a'.repeat(43);
    element('mp-public-key').value = scenario === 'private-key' ? '-----BEGIN PRIVATE KEY-----' :
      scenario === 'no-key' || scenario === 'local' ? '' : 'ssh-ed25519 AAAA';
    await element('mp-submit').listeners.click();
    const actions = calls.slice(1).map(([verb, noun]) => `${verb} ${noun}`);
    if (scenario === 'private-key') {
      assert.deepEqual(actions, []);
      assert.match(element('mp-onboarding-status').textContent, /not a private key/);
    } else if (scenario === 'trust-failed') {
      assert.deepEqual(actions, ['add mi6-key']);
      assert.match(element('mp-onboarding-status').textContent, /registration not attempted/);
    } else if (scenario === 'registration-failed' || scenario === 'connectivity-failed') {
      assert.deepEqual(actions, ['add mi6-key', 'add moneypenny']);
      assert.match(element('mp-onboarding-status').textContent, /remains authorized/);
      assert.equal(element('mp-submit').disabled, false);
      if (scenario === 'connectivity-failed') assert.match(element('mp-onboarding-status').textContent, /connectivity check failed/);
    } else {
      assert.deepEqual(actions, scenario === 'no-key' || scenario === 'local' ?
        ['add moneypenny'] : ['add mi6-key', 'add moneypenny']);
      assert.equal(element('mp-submit').disabled, true);
      assert.match(element('mp-onboarding-status').textContent, /Connectivity: reachable/);
    }
    assert.ok(!actions.some(action => action.startsWith('delete')));
    const trust = calls.find(([, noun]) => noun === 'mi6-key');
    if (trust) assert.deepEqual(trust[2], ['ssh-ed25519 AAAA', '--mi6', 'relay:443', '--mi6-server-fingerprint', 'SHA256:' + 'a'.repeat(43)]);
  }
});

test('dashboard completion selects the next visible session before the request', () => {
  for (const [ids, selected, expected] of [
    [['a', 'b', 'c'], 'b', 'c'],
    [['a', 'b'], 'b', 'a'],
    [['a'], 'a', ''],
    [['a', 'c'], 'a', 'c'],
  ]) {
    const calls = [];
    const context = vm.createContext({
      dashEntries: ids.map(sessionId => ({ sessionId })),
      dashSelectedId: selected,
      applyDashSelection() { calls.push(['selection', context.dashSelectedId]); },
      completeSession(id) { calls.push(['complete', id, context.dashSelectedId]); },
    });
    const source = app.slice(app.indexOf('  function dashAction(cmd)'), app.indexOf('  // --- Project filter ---'));
    vm.runInContext(`${source}\ndashAction('complete');`, context);
    assert.deepEqual(calls, [['selection', expected], ['complete', selected, expected]]);
  }
  assert.match(app, /c: \(\) => dashAction\('complete'\), C: \(\) => dashAction\('complete'\)/);
});

test('agent-authored messages have their own icon and red role style', () => {
  const container = { innerHTML: '', scrollHeight: 0, scrollTop: 0, clientHeight: 0 };
  const context = vm.createContext({
    document: { getElementById: () => container },
    chatConversation: [
      { role: 'user', content: 'agent prompt', source_session_id: 'sender', source_name: 'cecil' },
      { role: 'user', content: 'human prompt' },
      { role: 'assistant', content: 'reply' },
    ],
    lastSubagents: [], lastActivity: [], queuedMessages: [], chatTotal: 3,
    currentSessionNick: '', currentSessionName: 'receiver', currentSessionStatus: 'idle',
    showThoughts: false, expandedActivity: false, userLabel: 'you',
    lastChatHTML: '', chatForceScrollBottom: false,
    collapseSupersededActivityTurns: turns => turns,
    formatContent: text => text,
    escapeHtml: text => text,
    ensureChatScrollable() {},
  });
  const source = app.slice(app.indexOf('  function renderChat(prepend)'), app.indexOf('  // --- Attachments ---'));
  vm.runInContext(`${source}\nrenderChat(false);`, context);
  assert.match(container.innerHTML, /msg-role agent-user">🕴️ cecil/);
  assert.match(container.innerHTML, /msg-role user">🧑‍💻 you/);
  assert.match(container.innerHTML, /msg-role assistant">🕴️ receiver/);
  const css = fs.readFileSync(path.join(__dirname, '../pkg/web/static/index.html'), 'utf8');
  assert.match(css, /\.msg-role\.agent-user\s*\{\s*color:\s*var\(--danger\)/);
});

function dashboardHarness() {
  let html = '';
  let rows = [];
  const container = {
    get innerHTML() { return html; },
    set innerHTML(value) {
      html = value;
      rows = [...value.matchAll(/class="session-row" data-session-id="([^"]+)"/g)].map(match => ({
        dataset: { sessionId: match[1] },
        setAttribute() {}, addEventListener() {},
      }));
    },
    scrollTop: 140,
    querySelector() { return rows[0] || null; },
    querySelectorAll() { return rows; },
  };
  const saved = new Map();
  const context = vm.createContext({
    document: { getElementById: () => container },
    localStorage: { setItem: (key, value) => saved.set(key, value) },
    escapeAttr: value => String(value).replaceAll('"', '&quot;'),
    escapeHtml: value => String(value).replaceAll('<', '&lt;'),
    relativeTime: () => '',
    fuzzyMatch: (query, target) => target.toLowerCase().includes(query.toLowerCase()),
    applyDashSelection() {},
    excludedProjects: new Set(), projectsCache: [], categoryDefaults: {}, categoryFilterOverrides: {}, projectFilterCursor: 0, projectFilterOptions: [],
    dashSelectedId: '',
    lastDashboardData: null,
    dashEntries: [],
    dashRowSigs: {},
    lastDashRenderSig: null,
    dashExpanded: new Set(), dashAllSessions: false, dashShowCompleted: false, dashGroupByProject: false,
  });
  const source = app.slice(app.indexOf('  function renderDashboard(data)'), app.indexOf('  // Open a dashboard entry'));
  const projectFilter = app.slice(app.indexOf('  function dashboardProjectOptions('), app.indexOf('  // fuzzyMatch returns'));
  const projectLabel = app.slice(app.indexOf('  function cacheProjects('), app.indexOf('  // --- Traits ---'));
  vm.runInContext(`${source}\n${projectFilter}\n${projectLabel}\nglobalThis.render = renderDashboard;`, context);
  return { context, container, saved };
}

test('project list displays server counts and stale state without changing saved status', async () => {
  const { context } = dashboardHarness();
  const inputs = ['display', 'override-filter'].map(setting => ({
    dataset: { categorySetting: setting, categoryIndex: '1' },
    checked: false, disabled: false,
    addEventListener(event, handler) { this[event] = handler; },
  }));
  const container = { innerHTML: '', querySelectorAll: selector => selector === '[data-category-setting]' ? inputs : [] };
  context.document.getElementById = () => container;
  const rows = [
    ['empty', 'Empty', 'active', '', '', '[]', 'Background', '0', '0', 'stale'],
    ['work', 'Work', 'active', '', '', '[]', 'Background', '2', '3', 'active'],
    ['done', 'Done', 'done', '', '', '[]', '', '0', '4', 'done'],
    ['old', 'Older server', 'active', '', '', '[]'],
  ];
  const defaults = { Background: false }, overrides = { Background: true }, updates = [], alerts = [];
  let fail = false;
  context.apiCall = async (verb, noun, args) => {
    if (verb === 'update') {
      updates.push(args);
      return fail ? { status: 'error', message: 'Save failed' } : { status: 'ok' };
    }
    return { status: 'ok', data: { rows, category_defaults: defaults, category_filter_overrides: overrides } };
  };
  context.loadProjects = async () => {};
  context.alert = message => alerts.push(message);
  context.applyMgmtCursor = () => {};
  const source = app.slice(app.indexOf('  async function loadProjectsList()'), app.indexOf('  async function deleteProject('));
  vm.runInContext(source, context);
  await context.loadProjectsList();
  assert.match(container.innerHTML, /mgmt-badge stale">stale/);
  assert.match(container.innerHTML, /0 active · 0 completed/);
  assert.match(container.innerHTML, /2 active · 3 completed/);
  assert.match(container.innerHTML, /0 active · 4 completed/);
  assert.match(container.innerHTML, /Older server/);
  assert.equal(context.projectsCache.find(p => p.name === 'Empty').status, 'active');
  assert.equal(context.excludedProjects.has('Empty'), true);
  assert.equal(context.excludedProjects.has('Done'), true);
  assert.ok(container.innerHTML.indexOf('data-project-name="Empty"') < container.innerHTML.indexOf('data-project-name="Work"'));
  assert.ok(container.innerHTML.indexOf('data-project-name="Older server"') < container.innerHTML.indexOf('class="project-category-heading">Background'));
  assert.match(container.innerHTML, /aria-label="Override filter Background"[^>]+checked/);
  for (const input of inputs) {
    input.checked = true;
    await input.change();
    assert.equal(input.disabled, false);
    assert.deepEqual(Array.from(updates.at(-1)), ['--name=Background', '--' + input.dataset.categorySetting + '=true']);
    fail = true;
    input.checked = false;
    await input.change();
    assert.equal(input.checked, true, 'failed save rolls the checkbox back');
    assert.equal(input.disabled, false);
    fail = false;
  }
  assert.equal(alerts.length, 2);
  assert.match(alerts[0], /Save failed/);
});

test('dashboard shows the selected model and refreshes the row when it changes', () => {
  const { context, container } = dashboardHarness();
  const data = model => ({ rows: [['session', 'Arnold', '', 'idle', 'mac', '', '', '', 'opencode', '', model]] });
  context.render(data('azure/gpt-6-luna'));
  const rowElement = container.querySelector();
  assert.match(container.innerHTML, /opencode · azure\/gpt-6-luna/);
  context.render(data('azure/gpt-6-sol'));
  assert.match(rowElement.innerHTML, /opencode · azure\/gpt-6-sol/);
  context.render(data(''));
  assert.match(rowElement.innerHTML, />opencode<\/span>/);
  assert.doesNotMatch(rowElement.innerHTML, /opencode ·/);
  context.render(data('<unsafe>'));
  assert.match(rowElement.innerHTML, /&lt;unsafe>/);
});

test('workstreams retain hierarchy, independent state, expansion, selection and filter context', () => {
  const { context, container, saved } = dashboardHarness();
  const data = { workstreams: [{
    root_session_id: 'parent', section: 'needs_you', active: 3, running: 1, unread: 1, unavailable: 0,
    sessions: [
      { session_id: 'parent', name: 'Research', nick: 'ian', execution: 'idle', depth: 0, moneypenny: 'mac' },
      { session_id: 'child', parent_session_id: 'parent', name: 'Collect', execution: 'working', depth: 1, moneypenny: 'mac' },
      { session_id: 'nested', parent_session_id: 'child', name: 'Findings', execution: 'idle', unread: true, depth: 2, moneypenny: 'mac' },
      { session_id: 'done', parent_session_id: 'parent', name: 'Archived', execution: 'idle', completed: true, depth: 1, moneypenny: 'mac' },
    ],
  }] };
  context.render(data);
  assert.deepEqual(Array.from(context.dashEntries, e => e.sessionId), ['parent']);
  assert.match(container.innerHTML, /Needs you/);
  assert.match(container.innerHTML, /<span class="session-nick">ian<\/span> Research/);
  assert.doesNotMatch(container.innerHTML, /workstream-nick/);
  assert.match(container.innerHTML, /3 active · 1 running · 1 unread/);
  assert.match(container.innerHTML, /class="session-status idle">idle<\/span>/);
  assert.doesNotMatch(container.innerHTML, /class="session-status working"/);
  assert.equal(container.scrollTop, 140);
  assert.equal(data.rows.length, 4, 'quick switcher cache retains collapsed descendants');
  context.toggleWorkstream('parent', true);
  assert.deepEqual(Array.from(context.dashEntries, e => e.sessionId), ['parent', 'child', 'nested']);
  assert.equal((container.innerHTML.match(/Needs you/g) || []).length, 1);
  assert.equal(saved.get('qewExpandedWorkstreams'), 'parent');
  context.dashSelectedId = 'nested';
  context.render(data);
  assert.equal(context.dashSelectedId, 'nested');
  context.toggleWorkstream('parent', false);
  assert.equal(context.dashSelectedId, 'parent');
  context.dashAllSessions = true;
  context.render(data);
  assert.equal(context.dashEntries.length, 3);
  context.dashShowCompleted = true;
  context.render(data);
  assert.equal(context.dashEntries.length, 4);
  // Completed ancestors are retained, but unrelated completed branches are not.
  data.workstreams[0].sessions[0].completed = true;
  data.workstreams[0].sessions.splice(3, 0,
    { session_id: 'completed-branch', parent_session_id: 'parent', name: 'Old branch', execution: 'idle', completed: true, depth: 1 });
  context.dashShowCompleted = false;
  context.render(data);
  assert.deepEqual(Array.from(context.dashEntries, e => e.sessionId), ['parent', 'child', 'nested']);
  data.workstreams[0].sessions[2].name = '<unsafe>';
  context.render(data);
  assert.match(container.querySelectorAll()[2].innerHTML, /&lt;unsafe>/);
});

function dashboardKeyboardHarness() {
  const calls = [];
  let listener, modal = false;
  const list = { tagName: 'DIV', focus() { context.document.activeElement = list; } };
  const button = { tagName: 'BUTTON' };
  const context = vm.createContext({
    currentSession: null, cmdPaletteOpen: false, overridePicker: null,
    dashEntries: [{ sessionId: 'parent', rootId: 'parent' }],
    dashSelectedId: 'parent',
    toggleWorkstream: (id, expanded) => calls.push([id, expanded]),
    activeMgmtList: () => null,
    document: {
      activeElement: list,
      getElementById: id => id === 'dash-content' ? list : { style: { display: 'flex' } },
      querySelector: selector => modal && (selector === '.modal-overlay' ||
        selector === '.cmd-palette[aria-label="Session list shortcuts"]') ? {} : null,
      addEventListener: (event, handler) => { listener = handler; },
    },
    window: {}, closeWizard: () => { modal = false; calls.push(['close']); },
    showDashboardShortcuts: () => { modal = true; context.document.activeElement = button; },
    dashMove: delta => calls.push(['move', delta]),
    dashOpenSelected: () => calls.push(['open']),
    dashAction: action => calls.push(['action', action]),
    openProjectFilter: () => calls.push(['filter']),
    clearProjectFilter: () => calls.push(['clear']),
    showMoneypenniesView() {}, showPonsonbiesView() {}, toggleSound() {},
    openCreateWizard: () => calls.push(['new']),
    showProjectsView() {}, openOmnibar() {}, showTraitsView() {},
  });
  vm.runInContext(app.slice(app.indexOf('  function focusDashboard('),
    app.indexOf('  function openAllSubagent(')), context);
  vm.runInContext(app.slice(app.indexOf("  document.addEventListener('keydown', (e) => {"),
    app.indexOf('  // --- Routing ---')), context);
  return { context, calls, list, button, press(key, extra = {}) {
    let prevented = false;
    listener({ key, target: context.document.activeElement, preventDefault() { prevented = true; }, ...extra });
    return prevented;
  } };
}

test('dashboard help executes list shortcuts and restores list focus on dismissal', () => {
  for (const [key, expected] of [['c', ['action', 'complete']], ['n', ['new']],
    ['/', ['filter']], ['j', ['move', 1]], ['Enter', ['open']], ['Escape', null]]) {
    const { context, calls, list, press } = dashboardKeyboardHarness();
    assert.equal(press('Escape'), true);
    for (const extra of [{ ctrlKey: true }, { metaKey: true }, { altKey: true },
      { repeat: true }, { isComposing: true }]) {
      assert.equal(press('c', extra), false);
      assert.deepEqual(calls, []);
    }
    assert.equal(press('z'), false);
    assert.equal(press(key), true);
    assert.deepEqual(calls, expected ? [['close'], expected] : [['close']]);
    assert.equal(context.document.activeElement, list);
    if (key === 'Escape') {
      assert.equal(press('Enter'), true);
      assert.deepEqual(calls.at(-1), ['open']);
    }
  }
  const { context, calls, button, press } = dashboardKeyboardHarness();
  context.document.activeElement = button;
  assert.equal(press('Enter'), false, 'ordinary toolbar buttons retain native Enter');
  assert.deepEqual(calls, []);
});

test('dashboard h/l reuse directional workstream folding shortcuts', () => {
  const { calls, press } = dashboardKeyboardHarness();
  for (const [key, expanded] of [['h', false], ['l', true], ['ArrowLeft', false], ['ArrowRight', true]]) {
    assert.equal(press(key), true);
    assert.deepEqual(calls.pop(), ['parent', expanded]);
  }
});

test('Manage project grouping affects only Quiet and preserves workstreams, selection and refresh', () => {
  const { context, container } = dashboardHarness();
  const workstream = (id, project, section = 'quiet', children = []) => ({
    root_session_id: id, section, active: 1 + children.length, running: section === 'in_progress' ? 1 : 0,
    sessions: [
      { session_id: id, name: id, project, depth: 0, moneypenny: 'mac', execution: 'idle' },
      ...children,
    ],
  });
  const data = { workstreams: [
    workstream('attention-z', 'Zulu', 'needs_you'),
    workstream('attention-a', 'Alpha', 'needs_you'),
    workstream('running-z', 'Zulu', 'in_progress'),
    workstream('running-a', 'Alpha', 'in_progress'),
    workstream('none', ''),
    workstream('zulu', 'Zulu'),
    workstream('alpha', 'Alpha', 'quiet', [
      { session_id: 'child', name: 'child', project: 'Zulu', depth: 1, moneypenny: 'mac', execution: 'idle' },
    ]),
    workstream('alpha-two', 'Alpha'),
    workstream('done', 'Alpha', 'completed'),
  ] };
  data.workstreams.at(-1).sessions[0].completed = true;
  const original = JSON.stringify(data.workstreams);
  context.render(data);
  assert.doesNotMatch(container.innerHTML, /session-project-heading/);
  const button = { addEventListener(event, handler) { this[event] = handler; }, setAttribute(key, value) { this[key] = value; } };
  context.document.getElementById = id => id === 'dash-group-project-btn' ? button : container;
  const start = app.indexOf("  document.getElementById('dash-group-project-btn').addEventListener");
  vm.runInContext(app.slice(start, app.indexOf("  document.getElementById('mp-back-btn')", start)), context);
  context.dashSelectedId = 'zulu';
  button.click({ currentTarget: button });
  assert.equal(button['aria-pressed'], 'true');
  assert.deepEqual(Array.from(context.dashEntries, e => e.sessionId), [
    'attention-z', 'attention-a', 'running-z', 'running-a', 'alpha', 'alpha-two', 'zulu', 'none',
  ]);
  assert.equal(context.dashSelectedId, 'zulu');
  assert.equal((container.innerHTML.match(/session-project-heading/g) || []).length, 3);
  assert.match(container.innerHTML, /session-project-heading">No project/);
  assert.ok(container.innerHTML.indexOf('session-project-heading') > container.innerHTML.indexOf('data-session-id="running-a"'));
  context.dashExpanded.add('alpha');
  context.render(data);
  assert.deepEqual(Array.from(context.dashEntries, e => e.sessionId).slice(4, 8), ['alpha', 'child', 'alpha-two', 'zulu']);
  assert.equal((container.innerHTML.match(/session-project-heading/g) || []).length, 3, 'child stays under root project');
  context.dashAllSessions = true;
  context.render(data);
  assert.equal((container.innerHTML.match(/session-project-heading/g) || []).length, 3);
  data.workstreams.find(w => w.root_session_id === 'alpha').sessions[0].project = '<Beta>';
  context.render(data);
  assert.match(container.innerHTML, /session-project-heading">&lt;Beta>/);
  button.click({ currentTarget: button });
  assert.equal(button['aria-pressed'], 'false');
  assert.doesNotMatch(container.innerHTML, /session-project-heading/);
  data.workstreams.find(w => w.root_session_id === 'alpha').sessions[0].project = 'Alpha';
  assert.equal(JSON.stringify(data.workstreams), original, 'grouping does not reorder authoritative workstreams');
});

test('project exclusions keep matching descendants, accurate counts and untouched cached data', () => {
  const { context, container } = dashboardHarness();
  const data = { workstreams: [{
    root_session_id: 'parent', section: 'needs_you', active: 3, unread: 1, running: 1, requests: 1,
    sessions: [
      { session_id: 'parent', name: 'Root', project: 'Hidden', depth: 0, execution: 'working',
        attention: { kind: 'needs_answer', reason: 'Excluded question', revision: 1 } },
      { session_id: 'child', name: 'Child', project: 'Keep', depth: 1, execution: 'working' },
      { session_id: 'other', name: 'Other', project: 'Hidden', depth: 1, execution: 'idle', unread: true },
      { session_id: 'none', name: 'Unassigned', depth: 1, execution: 'idle' },
      { session_id: 'archived', project: 'Archived', depth: 1, completed: true, execution: 'idle' },
    ],
  }] };
  const original = JSON.stringify(data);
  context.excludedProjects.add('Hidden');
  context.excludedProjects.add('');
  const filtered = context.filterDashboardProjects(data);
  assert.equal(JSON.stringify(data), original);
  assert.deepEqual(Array.from(filtered.workstreams[0].sessions, s => s.session_id), ['parent', 'child']);
  assert.equal(filtered.workstreams[0].active, 1);
  assert.equal(filtered.workstreams[0].unread, 0);
  assert.equal(filtered.workstreams[0].requests, 0);
  assert.equal(filtered.workstreams[0].section, 'in_progress');
  assert.equal(filtered.workstreams[0].sessions[0].attention, null);
  assert.equal(filtered.workstreams[0].sessions[0].projectContext, true);
  data.workstreams[0].sessions[1].execution = 'idle';
  context.render(data);
  assert.match(container.innerHTML, /Quiet/);
  assert.match(container.innerHTML, /Parent context · Hidden \(excluded\)/);
  assert.match(container.innerHTML, />context<\/span>/);
  assert.doesNotMatch(container.innerHTML, /session-status working|Excluded question/);
  context.excludedProjects.clear();
  context.render(data);
  assert.doesNotMatch(container.innerHTML, /Parent context|>context<\/span>/);
  assert.match(container.innerHTML, /session-status working/);
  context.excludedProjects.add('Hidden');
  context.excludedProjects.add('');
  context.excludedProjects.add('Keep');
  assert.equal(context.filterDashboardProjects(data).workstreams.length, 0);
  context.dashShowCompleted = true;
  assert.deepEqual(Array.from(context.filterDashboardProjects(data).workstreams[0].sessions, s => s.session_id), ['parent', 'archived']);
  assert.deepEqual(Array.from(context.filterDashboardProjects({ rows: [
    ['one', '', 'Keep'], ['two', '', 'Hidden'], ['three', '', ''],
  ] }).rows), []);
  context.excludedProjects.clear();
  assert.equal(context.filterDashboardProjects(data), data);
});

test('category attention override reveals only unresolved requests and restores exclusions after resolution', () => {
  const { context } = dashboardHarness();
  context.cacheProjects([
    ['p', 'Hidden', 'active', '', '', '', 'Background'],
    ['q', 'Other', 'active', '', '', '', 'Private'],
  ], { Background: false, Private: false }, { Background: true });
  context.excludedProjects.add('');
  const parent = { session_id: 'parent', project: 'Hidden', depth: 0, execution: 'working', unread: true };
  const child = { session_id: 'child', project: 'Hidden', depth: 1, completed: true, execution: 'idle',
    attention: { kind: 'failed', reason: 'Background failed', revision: 1 } };
  const data = { workstreams: [{
    root_session_id: 'parent', sessions: [
      parent, child,
      { session_id: 'sibling', project: 'Hidden', depth: 1, execution: 'idle' },
      { session_id: 'private', project: 'Other', depth: 1, attention: { kind: 'needs_action' } },
    ],
  }] };
  for (const kind of ['failed', 'needs_answer', 'needs_action']) {
    child.attention.kind = kind;
    const before = JSON.stringify(data);
    const filtered = context.filterDashboardProjects(data);
    assert.equal(JSON.stringify(data), before, 'projection must not mutate authoritative data');
    const w = filtered.workstreams[0];
    assert.deepEqual(Array.from(w.sessions, s => s.session_id), ['parent', 'child']);
    assert.equal(w.sessions[0].projectContext, true);
    assert.equal(w.sessions[0].attention, null);
    assert.equal(w.sessions[1].attention.kind, kind);
    assert.equal(w.sessions[1].projectContext, undefined);
    assert.equal(w.requests, 1);
    assert.equal(w.active, 0);
    assert.equal(w.running, 0);
    assert.equal(w.unread, 0);
    assert.equal(w.section, 'needs_you');
    assert.equal(context.excludedProjects.has('Hidden'), true, 'attention does not select the project');
  }
  context.cacheProjects([
    ['p', 'Hidden', 'active', '', '', '', 'Background'],
    ['q', 'Other', 'active', '', '', '', 'Private'],
  ], { Background: false, Private: false });
  assert.equal(context.filterDashboardProjects(data).workstreams.length, 0, 'override defaults off');
  context.categoryFilterOverrides.Background = true;
  child.attention = null;
  assert.equal(context.filterDashboardProjects(data).workstreams.length, 0, 'unread/running are not explicit requests');
  context.categoryFilterOverrides[''] = true;
  parent.project = '';
  parent.attention = { kind: 'needs_action' };
  assert.equal(context.filterDashboardProjects(data).workstreams[0].requests, 1, 'Uncategorized includes No project');
});

test('project picker groups categories, folds completed projects and supports batch and individual selection', () => {
  const { context, container } = dashboardHarness();
  let controls = [], rows = [], open = false, focus, markup = '';
  let completed;
  const buttons = new Map();
  const button = id => {
    if (!buttons.has(id)) buttons.set(id, {
      addEventListener(event, handler) { this[event] = handler; },
      focus() { focus = id; },
    });
    return buttons.get(id);
  };
  const list = {
    closest: () => ({ setAttribute() {} }),
    querySelectorAll: selector => {
      if (selector === '.mgmt-row') return rows;
      if (selector === 'input, summary') return controls;
      if (selector === 'input') return controls.filter(input => input.tagName === 'INPUT');
      return controls.filter(input => selector === '[data-project-index]'
        ? input.dataset.projectIndex !== undefined : input.dataset.projectGroup !== undefined);
    },
  };
  container.focus = () => { focus = container; };
  vm.runInContext(app.slice(app.indexOf('  function focusDashboard('),
    app.indexOf('  window._qewCloseDashboardShortcuts')), context);
  context.document.getElementById = id => id === 'dash-content' ? container
    : id === 'project-filter-list' ? open ? list : null : button(id);
  context.closeWizard = () => { open = false; };
  let modalClass;
  context.renderWizardModal = (content, className) => {
    open = true;
    markup = content;
    modalClass = className;
    completed = { open: false };
    const completedStart = content.indexOf('<details');
    controls = [...content.matchAll(/<input type="checkbox" data-project-(index|group)="(\d+)"[^>]*>|<summary[^>]*>/g)].map(match => {
      const summary = match[0].startsWith('<summary');
      const row = { classList: { toggle() {}, add() {}, remove() {} }, scrollIntoView() {} };
      const handlers = {};
      return {
        tagName: summary ? 'SUMMARY' : 'INPUT',
        checked: match[0].includes(' checked'),
        dataset: summary ? {} : { [match[1] === 'index' ? 'projectIndex' : 'projectGroup']: match[2] },
        closest: selector => selector === 'details' ? match.index > completedStart ? completed : null : row,
        addEventListener(event, handler) { handlers[event] = handler; },
        focus() { focus = this; handlers.focus?.(); },
        click() {
          if (summary) completed.open = !completed.open;
          else { this.checked = !this.checked; handlers.change(); }
        },
      };
    });
    rows = controls.map(input => input.closest('.mgmt-row'));
  };
  const projects = [
    ['a', 'Alpha', 'active', '', '', '', 'Work'],
    ['b', '<Beta>', 'done', '', '', '', 'Work'],
    ['c', 'Gamma', 'paused', '', '', '', 'Work'],
  ];
  context.cacheProjects(projects);
  const data = { rows: [['one', 'One', 'Alpha'], ['two', 'Two', '<Beta>'], ['three', 'Three', ''], ['four', 'Four', 'Gamma']] };
  context.render(data);
  assert.equal(context.excludedProjects.has('<Beta>'), true, 'completed projects default off before opening picker');
  const press = (key, extra = {}) => {
    let prevented = false;
    assert.equal(context.handleProjectFilterKey({ key, preventDefault() { prevented = true; }, ...extra }), true);
    return prevented;
  };
  context.openProjectFilter();
  const project = name => controls.find(input => context.projectFilterOptions[Number(input.dataset.projectIndex)] === name);
  const groups = () => controls.filter(input => input.dataset.projectGroup !== undefined);
  assert.equal(modalClass, 'modal-project-filter');
  assert.match(markup, /No project/);
  assert.match(markup, /&lt;Beta>/);
  assert.match(markup, /Completed projects/);
  assert.ok(markup.indexOf('Completed projects') > markup.indexOf('Gamma'));
  assert.equal(completed.open, false);
  assert.equal(context.projectFilterControls().includes(project('<Beta>')), false);
  assert.equal(focus, groups()[0]);
  assert.equal(press(' ', { target: { tagName: 'BUTTON' } }), false, 'buttons retain native Space activation');
  press('k');
  assert.equal(focus, groups()[0]);
  press('j');
  assert.equal(focus, project(''));
  press(' ');
  assert.equal(context.excludedProjects.has(''), true);
  assert.deepEqual(Array.from(context.dashEntries, e => e.sessionId), ['one', 'four']);
  press(' ', { repeat: true });
  assert.equal(project('').checked, false, 'held Space must not repeatedly toggle');
  press('j');
  const work = focus;
  assert.equal(work, groups()[1]);
  press(' ');
  assert.equal(context.dashEntries.length, 0, 'category toggles all its active and paused projects');
  project('Alpha').click();
  assert.equal(work.indeterminate, true);
  work.click();
  assert.equal(work.indeterminate, false);
  assert.equal(project('Gamma').checked, true);
  assert.equal(project('<Beta>').checked, false, 'active category does not select hidden completed members');
  for (let i = 0; i < controls.length && focus.tagName !== 'SUMMARY'; i++) press('j', { repeat: true });
  assert.equal(focus.tagName, 'SUMMARY', 'navigation reaches completed fold without entering hidden rows');
  press(' ');
  assert.equal(completed.open, true);
  press('j');
  press(' ');
  assert.equal(project('<Beta>').checked, true, 'completed category can be selected explicitly');
  press('Escape');
  assert.equal(open, false);
  assert.equal(focus, container);
  context.openProjectFilter();
  assert.equal(completed.open, false, 'completed fold resets on reopen');
  assert.equal(project('<Beta>').checked, true, 'closing preserves explicit choices');
  press('q');
  assert.equal(open, true, 'clear does not close the picker');
  assert.equal(context.excludedProjects.size, 0);
  assert.equal(context.dashEntries.length, 4);
  context.cacheProjects(projects);
  assert.equal(context.excludedProjects.size, 0, 'refresh does not undo explicit completed selection');
  press('n');
  assert.equal(open, true, 'deselect all does not close the picker');
  assert.ok(controls.filter(input => input.tagName === 'INPUT').every(input => !input.checked));
  assert.equal(context.dashEntries.length, 0);
  press('j');
  press(' ');
  assert.equal(context.dashEntries.length, 1, 'a project can be enabled after deselecting all');
  press('n', { repeat: true });
  assert.equal(context.dashEntries.length, 1, 'held n is ignored');
  buttons.get('project-filter-none').click();
  assert.equal(context.dashEntries.length, 0, 'mouse action also deselects all');
  press('q');
  assert.equal(context.dashEntries.length, 4);
  press('d');
  assert.equal(context.excludedProjects.has('<Beta>'), true, 'defaults exclude completed projects again');
  assert.equal(context.dashEntries.length, 3);
  context.categoryDefaults.Work = false;
  buttons.get('project-filter-default').click();
  assert.deepEqual([...context.excludedProjects].sort(), ['<Beta>', 'Alpha', 'Gamma']);
  assert.equal(context.dashEntries.length, 1, 'defaults restore category Display settings');
  press('q');
  press('Escape');
  context.selectOnlyProject('Alpha');
  assert.deepEqual(Array.from(context.dashEntries, e => e.sessionId), ['one']);
  context.clearProjectFilter();
  assert.equal(context.dashEntries.length, 4);
  context.cacheProjects(projects.map(row => row[1] === 'Alpha' ? [row[0], row[1], 'done', ...row.slice(3)] : row));
  assert.equal(context.excludedProjects.has('Alpha'), true, 'newly completed projects default off');
  assert.equal(context.excludedProjects.has('<Beta>'), false, 'existing explicit completed selection is retained');
  assert.doesNotMatch(app, /id="dash-filter"|function openDashFilter|function initDashFilter/);
  assert.match(app, /'\/': openProjectFilter, q: clearProjectFilter/);
});

test('category Display defaults initialize filters without undoing explicit page selections on refresh', () => {
  const { context } = dashboardHarness();
  const rows = [
    ['a', 'Alpha', 'active', '', '', '', 'Work'],
    ['b', 'Beta', 'done', '', '', '', 'Work'],
    ['n', 'Notes', 'active', '', '', '', 'Personal'],
    ['l', 'Loose', 'active', '', '', '', ''],
  ];
  context.cacheProjects(rows, { Work: false, '': false });
  assert.deepEqual([...context.excludedProjects].sort(), ['', 'Alpha', 'Beta', 'Loose']);
  context.clearProjectFilter();
  context.cacheProjects(rows, { Work: false, '': false });
  assert.equal(context.excludedProjects.size, 0, 'manual Show all survives refresh');
  context.cacheProjects([...rows, ['x', 'New work', 'active', '', '', '', 'Work']], { Work: false, '': false });
  assert.equal(context.excludedProjects.has('New work'), true, 'new project inherits category default');
  context.cacheProjects(rows, { Work: true, '': true });
  assert.equal(context.excludedProjects.has('Alpha'), false);
  context.cacheProjects(rows, { Work: false });
  assert.equal(context.excludedProjects.has('Alpha'), true, 'changing the shared default reapplies it');
  context.cacheProjects(rows, { Work: true });
  assert.equal(context.excludedProjects.has('Alpha'), false);
  assert.equal(context.excludedProjects.has(''), false);
  context.cacheProjects(rows.map(row => row[1] === 'Notes' ? [...row.slice(0, 6), 'Hidden'] : row), { Hidden: false });
  assert.equal(context.excludedProjects.has('Notes'), true, 'moving a project into a hidden category applies its default');
  context.cacheProjects(rows, { Hidden: false });
  assert.equal(context.excludedProjects.has('Notes'), false, 'moving to a displayed category restores default visibility');
});

test('collapsed workstreams show and refresh child requests, including completed blockers', () => {
  const { context, container } = dashboardHarness();
  const child = { session_id: 'child', name: 'Deploy', depth: 1, completed: true, execution: 'idle',
    attention: { kind: 'needs_answer', reason: 'Which <host>?', revision: 1 } };
  const data = { workstreams: [{
    root_session_id: 'parent', section: 'needs_you', active: 0, running: 0, unread: 0, requests: 1,
    sessions: [{ session_id: 'parent', name: 'Rollout', summary: 'Checking deployment', depth: 0, completed: true, execution: 'idle' }, child],
  }] };
  context.render(data);
  assert.match(container.innerHTML, /1 need attention/);
  assert.match(container.innerHTML, /Checking deployment/);
  assert.match(container.innerHTML, /Deploy · Needs answer: Which &lt;host>\?/);
  child.attention = { kind: 'needs_action', reason: 'Sign in.', revision: 2 };
  context.render(data);
  assert.match(container.querySelector().innerHTML, /Deploy · Needs action: Sign in\./);
  context.dashExpanded.add('parent');
  context.render(data);
  assert.deepEqual(Array.from(context.dashEntries, e => e.sessionId), ['parent', 'child']);
});

test('dashboard falls back only when the server does not support the workstream view', async () => {
  for (const message of ['flag provided but not defined: -view', 'database unavailable']) {
    const calls = [];
    const rendered = [];
    const errors = [];
    const context = vm.createContext({
      dashboardLegacy: false, dashboardRefresh: null, projectFilter: '',
      lastSessionStates: {}, lastDashboardData: null,
      document: { getElementById: () => ({ style: {} }) },
      apiCall: async (verb, noun, args) => {
        calls.push(Array.from(args));
        return calls.length === 1 ? { status: 'error', message } : { status: 'ok', data: { rows: [] } };
      },
      renderDashboard: data => rendered.push(data), showDashboardError: error => errors.push(error),
      setConnectionState() {}, clearBackendRecovery() {}, scheduleBackendRecovery() {},
    });
    vm.runInContext(app.slice(app.indexOf('  function loadDashboard()'), app.indexOf('  function showDashboardError(')), context);
    await context.loadDashboard();
    assert.ok(calls[0].includes('--view=workstreams'));
    if (message.includes('-view')) {
      assert.equal(calls.length, 2);
      assert.ok(!calls[1].some(arg => arg.includes('--view')));
      assert.equal(rendered.length, 1);
      await context.loadDashboard();
      assert.ok(!calls[2].some(arg => arg.includes('--view')));
    } else {
      assert.equal(calls.length, 1);
      assert.equal(rendered.length, 0);
      assert.deepEqual(errors, [message]);
    }
  }
});

test('model picker refreshes a cold cache and surfaces discovery failures', async () => {
  const calls = [];
  const responses = [
    { status: 'ok', data: { models: [] } },
    { status: 'ok', data: { models: [{ name: 'Azure Luna', value: 'azure/GPT-5.6-luna' }] } },
    { status: 'ok', data: { models: [{ name: 'Cached', value: 'azure/cached' }] } },
    { status: 'ok', data: { models: [] } },
    { status: 'error', message: 'opencode model discovery returned no models' },
  ];
  const context = vm.createContext({
    apiCall: async (verb, noun, args) => {
      calls.push({ verb, noun, args: Array.from(args) });
      return responses.shift();
    },
    escapeHtml: value => value,
  });
  const source = app.slice(app.indexOf('  // Hem returns an empty list immediately'), app.indexOf('  // populateOverrideSelects'));
  vm.runInContext(`${source}\nglobalThis.loadModels = loadModels;`, context);
  assert.deepEqual(Array.from(await context.loadModels('personal', 'opencode')), ['azure/GPT-5.6-luna']);
  assert.deepEqual(calls.map(call => call.args), [
    ['-m', 'personal', '--agent', 'opencode'],
    ['-m', 'personal', '--agent', 'opencode', '--refresh'],
  ]);
  assert.deepEqual(Array.from(await context.loadModels('personal', 'opencode')), ['azure/cached']);
  assert.deepEqual(calls[2].args, ['-m', 'personal', '--agent', 'opencode']);
  await assert.rejects(context.loadModels('personal', 'opencode'), /opencode model discovery returned no models/);
});

// Run the application's refresh and socket handlers, substituting only browser
// services, API responses and rendering so timer behavior is deterministic.
function browser({ stableReconcile = false, reconcileRevision = 1, pushFirst = true, storage = new Map() } = {}) {
  const intervals = new Map();
  const timeouts = new Map();
  const calls = [];
  const rendered = [];
  const sockets = [];
  const socketMessages = [];
  const notifications = [], errors = [];
  let timerID = 0;
  let history = { conversation: [], total: 0 };
  let report = {};
  let pending = null;
  let rejectNext = false;
  const elements = new Map();
  const context = vm.createContext({
    localStorage: { getItem: key => storage.get(key) || null, setItem: (key, value) => storage.set(key, value) },
    console,
    currentSessionName: 'Agent',
    window: { location: { protocol: 'https:', host: 'qew.test' }, jamesCompactionThreshold: thresholds },
    document: {
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, { style: {}, innerHTML: '', addEventListener(event, handler) { this[event] = handler; } });
        return elements.get(id);
      },
    },
    WebSocket: class {
      static OPEN = 1;
      static CONNECTING = 0;
      constructor() { this.readyState = 0; sockets.push(this); }
      send(message) { socketMessages.push(JSON.parse(message)); }
      close() { this.readyState = 3; this.onclose(); }
    },
    setInterval(fn, ms) { const id = ++timerID; intervals.set(id, { fn, ms }); return id; },
    clearInterval(id) { intervals.delete(id); },
    setTimeout(fn, ms) { const id = ++timerID; timeouts.set(id, { fn, ms }); return id; },
    clearTimeout(id) { timeouts.delete(id); },
    apiCall(verb, noun, args) {
      calls.push({ verb, noun, args: Array.from(args) });
      if (rejectNext) { rejectNext = false; return Promise.reject(new Error('offline')); }
      if (verb === 'resolve') {
        if (Number(args[2]) !== report.attention.revision) {
          return Promise.resolve({ status: 'error', message: 'attention changed; refresh before updating' });
        }
        report = { attention: { revision: report.attention.revision + 1, kind: '' } };
        return Promise.resolve({ status: 'ok', data: report });
      }
      const response = verb === 'history'
        ? { status: 'ok', data: { ...history, ...report } }
        : verb === 'reconcile' && stableReconcile
          ? { status: 'ok', data: { session_id: 'session', conversation: null, total: 1, revision: reconcileRevision, generation: 1, ...report } }
          : { status: 'ok', data: verb === 'show' ? { name: 'Other agent', ...report } : { rows: [] } };
      if (pending) {
        const blocked = pending;
        pending = null;
        return blocked.then(() => response);
      }
      return Promise.resolve(response);
    },
    optionalCall: async () => null,
    syncSchedulesToggle() {},
    setConnectionState() {},
    mergeRecentHistory(data) { context.latestHistory = data; },
    renderChat() { rendered.push(context.latestHistory); },
    renderDashboard() {},
    showNotification: (...args) => notifications.push(args),
    alert: message => errors.push(message),
    escapeHtml: value => String(value).replaceAll('<', '&lt;'),
  });
  const source = pushFirst ? app : app.replace('const PUSH_FIRST = true;', 'const PUSH_FIRST = false;');
  const state = source.slice(source.indexOf("'use strict';"), source.indexOf('  function setConnectionState'));
  const ack = source.slice(source.indexOf('  function acknowledgeRenderedSnapshot'), source.indexOf('  // Optional panels'));
  const dashboard = source.slice(source.indexOf('  // --- Dashboard ---'), source.indexOf('  function renderDashboard'));
  const attention = source.slice(source.indexOf('  function attentionLabel('), source.indexOf('  function toggleWorkstream('));
  const chat = source.slice(source.indexOf('  function renderAttentionBanner('), source.indexOf('  // mergeRecentHistory'));
  const polling = source.slice(source.indexOf('  // --- Polling ---'), source.indexOf('  // --- Helpers ---'));
  vm.runInContext(`${state}\n${ack}\n${dashboard}\n${attention}\n${chat}\n${polling}
    globalThis.controls = {
      loadChat, loadDashboard, connectPushStream, startChatPoll, startDashboardPoll,
      resolveAttention,
      acknowledgeRenderedSnapshot,
      navigate(id) {
        currentSession = id;
        if (typeof chatGeneration !== 'undefined') chatGeneration++;
        if (typeof generation !== 'undefined') generation = chatGeneration;
        requestActiveWatch();
      },
      cachePanels() {
        currentSessionStatus = 'working';
        lastActivity = [{ summary: 'last good activity' }];
        lastSchedules = [{ id: 1 }];
        allSubagents = [{ sessionId: 'child', status: 'working' }];
      },
      seedDashboard() {
        lastDashboardData = { rows: [['session', 'Agent', '', 'ready', 'mp']] };
      },
      dashboardHTML() { return document.getElementById('dash-content').innerHTML; },
      panels() { return { currentSessionStatus, lastActivity, lastSchedules, allSubagents }; }
    };`, context);
  return {
    ...context.controls, calls, rendered, sockets, socketMessages, notifications, errors,
    report(value) { report = value; },
    banner() { return elements.get('chat-attention'); },
    history(content) {
      history = {
        conversation: [{ role: 'assistant', content }],
        total: 1,
        ...(stableReconcile ? { revision: 1, generation: 1 } : {}),
      };
    },
    blockNext() { let release; pending = new Promise(resolve => { release = resolve; }); return release; },
    failNext() { rejectNext = true; },
    async tick(ms) {
      for (const timer of [...intervals.values()].filter(timer => timer.ms === ms)) await timer.fn();
    },
    timers(ms) { return [...intervals.values()].filter(timer => timer.ms === ms).length; },
    timeouts(ms) { return [...timeouts.values()].filter(timer => timer.ms === ms).length; },
    async fireTimeout(ms) {
      const timers = [...timeouts.entries()].filter(([, timer]) => timer.ms === ms);
      for (const [id, timer] of timers) {
        timeouts.delete(id);
        await timer.fn();
      }
    },
    openSocket() {
      context.controls.connectPushStream();
      const socket = sockets.at(-1);
      socket.readyState = 1;
      socket.onopen();
      return socket;
    },
  };
}

test('durable attention survives reads, deduplicates delivery and resolves only the current revision', async () => {
  const storage = new Map();
  const page = browser({ stableReconcile: true, storage });
  const question = { attention: { kind: 'needs_answer', reason: 'Which <region>?', revision: 1 } };
  page.report(question);
  page.navigate('session');
  page.history('Ready to deploy');
  await page.loadChat();
  assert.equal(page.banner().hidden, false);
  assert.match(page.banner().innerHTML, /Needs answer.*Which &lt;region>\?/s);
  assert.equal(page.notifications.length, 1);
  await page.loadChat();
  assert.equal(page.notifications.length, 1);
  assert.equal(page.calls.filter(c => c.verb === 'history').length, 1, 'unchanged metadata did not fetch history');
  assert.equal(page.banner().hidden, false, 'reading did not clear the question');
  const newer = { attention: { kind: 'needs_action', reason: 'Sign in.', revision: 2 } };
  page.report(newer);
  await page.resolveAttention();
  await page.loadChat();
  assert.match(page.errors[0], /attention changed/);
  assert.match(page.banner().innerHTML, /Needs action.*Sign in/s);
  assert.equal(page.notifications.length, 2);
  await page.resolveAttention();
  await page.loadChat();
  assert.equal(page.banner().hidden, true);
  assert.equal(page.notifications.length, 2, 'resolution did not alert');
  assert.deepEqual(page.calls.filter(c => c.verb === 'resolve').map(c => c.args),
    [['session', '--revision', '1'], ['session', '--revision', '2']]);
  // Reload/reconnect can still display the request, but cannot replay delivery.
  const reloaded = browser({ stableReconcile: true, storage });
  reloaded.navigate('session');
  reloaded.report(newer);
  await reloaded.loadChat();
  assert.equal(reloaded.banner().hidden, false);
  assert.equal(reloaded.notifications.length, 0);
  const socket = reloaded.openSocket();
  reloaded.report({ attention: { kind: 'failed', reason: 'Provider unavailable', revision: 1 } });
  socket.onmessage({ data: JSON.stringify({ event: 'session_attention', session_id: 'other' }) });
  await new Promise(setImmediate);
  assert.ok(reloaded.calls.some(c => c.verb === 'show' && c.args[0] === 'other'));
  assert.deepEqual(reloaded.notifications[0], ['Other agent', 'other', 'Failed', 'Provider unavailable']);
});

test('attention popovers retain the session name and tolerate unavailable sound', async () => {
  const popovers = [], opened = [], warnings = [];
  const context = vm.createContext({
    soundEnabled: true,
    Audio: class { async play() { throw new Error('autoplay unavailable'); } },
    Notification: class { static permission = 'denied'; },
    console: { warn: (...args) => warnings.push(args) },
    document: {
      createElement: () => ({ addEventListener(event, handler) { this[event] = handler; }, remove() {} }),
      body: { appendChild: popover => popovers.push(popover) },
    },
    setTimeout() {},
    openChat: (...args) => opened.push(args),
  });
  vm.runInContext(app.slice(app.indexOf('  async function playNotificationSound('), app.indexOf('  function toggleSound(')), context);
  context.showNotification('Deploy', 'session', 'Needs answer', 'Which <host>?');
  await new Promise(setImmediate);
  assert.equal(warnings.length, 1);
  assert.equal(popovers[0].textContent, 'Needs answer: Deploy: Which <host>?');
  popovers[0].click();
  assert.deepEqual(opened[0], ['session', 'Deploy']);
  context.soundEnabled = false;
  for (const notification of [
    undefined,
    class { static permission = 'granted'; constructor() { throw new Error('system notification unavailable'); } },
    class { static permission = 'default'; static async requestPermission() { throw new Error('permission unavailable'); } },
  ]) {
    context.Notification = notification;
    await context.showNotification('Deploy', 'session', 'Needs action', 'Sign in.');
    assert.equal(popovers.at(-1).textContent, 'Needs action: Deploy: Sign in.');
  }
  assert.equal(warnings.length, 3, 'unavailable optional delivery is reported without failing the view');
});

test('unchanged reconcile polls do not fetch history or change the transcript', async () => {
  const page = browser({ stableReconcile: true, pushFirst: false });
  page.navigate('session');
  page.history('initial');
  page.startChatPoll();
  await page.loadChat();
  const initialHistoryCalls = page.calls.filter(call => call.verb === 'history').length;
  assert.equal(initialHistoryCalls, 1);
  page.history('should not replace unchanged transcript');
  await page.tick(3000);
  await page.tick(3000);
  assert.equal(page.calls.filter(call => call.verb === 'history').length, initialHistoryCalls);
  assert.equal(page.calls.filter(call => call.verb === 'reconcile').length, 2);
  assert.equal(page.rendered.at(-1).conversation[0].content, 'initial');
});

test('changed revision hint triggers immediate reconcile', async () => {
  const page = browser({ stableReconcile: true, reconcileRevision: 2 });
  page.navigate('session');
  page.history('initial');
  await page.loadChat();
  const before = page.calls.length;
  const socket = page.openSocket();
  socket.onmessage({ data: JSON.stringify({
    event: 'chat_message', session_id: 'session',
    data: { revision: 2, generation: 1 },
  }) });
  await Promise.resolve();
  assert.equal(page.calls.length, before + 1);
  assert.equal(page.calls.at(-1).verb, 'reconcile');
});

test('stale and duplicate revision hints are suppressed', async () => {
  const page = browser({ stableReconcile: true, reconcileRevision: 1 });
  page.navigate('session');
  page.history('initial');
  await page.loadChat();
  const socket = page.openSocket();
  await page.loadChat();
  const before = page.calls.length;
  for (const revision of [1, 0]) {
    socket.onmessage({ data: JSON.stringify({
      event: 'chat_message', session_id: 'session',
      data: { revision, generation: 1 },
    }) });
  }
  await Promise.resolve();
  assert.equal(page.calls.length, before);
});

test('explicit resync marker triggers immediate reconcile', async () => {
  const page = browser({ stableReconcile: true, reconcileRevision: 1 });
  page.navigate('session');
  page.history('initial');
  await page.loadChat();
  const socket = page.openSocket();
  await page.loadChat();
  const before = page.calls.length;
  socket.onmessage({ data: JSON.stringify({ event: 'resync_required' }) });
  await Promise.resolve();
  assert.equal(page.calls.length, before + 1);
  assert.equal(page.calls.at(-1).verb, 'reconcile');
});

for (const withSocket of [false, true]) {
  test(`chat advances without hints (${withSocket ? 'healthy WebSocket' : 'no push backend'})`, async () => {
    const page = browser({ pushFirst: !withSocket, stableReconcile: withSocket });
    page.navigate('session');
    page.history('initial');
    page.startChatPoll();
    if (withSocket) page.openSocket();
    await page.loadChat();
    page.history('new reply');
    await page.tick(withSocket ? 60000 : 3000);
    if (withSocket) {
      assert.equal(page.calls.filter(call => call.verb === 'history').length, 1);
      assert.equal(page.timers(60000), 0);
    } else {
      assert.ok(page.timers(3000) <= 1);
    }
  });
}

test('healthy socket is timer-silent and reconnect does not duplicate recovery timers', async () => {
  const page = browser();
  page.startDashboardPoll();
  const socket = page.openSocket();
  await page.loadDashboard();
  const before = page.calls.length;
  await page.tick(60000);
  assert.equal(page.calls.length, before);
  socket.close();
  page.openSocket();
  await page.loadDashboard();
  assert.equal(page.timers(60000), 0);
});

test('healthy push socket retries an active chat after its authoritative read fails', async () => {
  const page = browser();
  page.navigate('session');
  page.history('initial');
  const socket = page.openSocket();
  await page.loadChat();
  page.failNext();
  await page.loadChat();
  assert.equal(socket.readyState, 1);
  assert.equal(page.timeouts(1000), 1);
  page.history('recovered');
  await page.fireTimeout(1000);
  assert.equal(page.rendered.at(-1).conversation[0].content, 'recovered');
  assert.equal(page.timeouts(1000), 0);
});

test('legacy mode keeps frequent polling cadence', async () => {
  const page = browser({ pushFirst: false });
  page.navigate('session');
  assert.ok(page.timers(3000) <= 1);
  page.navigate('');
  page.startDashboardPoll();
  assert.ok(page.timers(5000) <= 1);
});

test('render acknowledgement is current-session and generation guarded', async () => {
  const page = browser({ pushFirst: false });
  page.navigate('session');
  page.history('rendered');
  await page.loadChat();
  page.acknowledgeRenderedSnapshot('session', { revision: 1, generation: 1 });
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(page.calls.filter(call => call.verb === 'ack').length, 1);
  page.navigate('other');
  await Promise.resolve();
  assert.equal(page.calls.filter(call => call.verb === 'ack').length, 1);
});

test('watch responses are generation-safe and session switches request a new watch', async () => {
  const page = browser({ pushFirst: false });
  page.navigate('session-a');
  const socket = page.openSocket();
  const firstRequest = page.socketMessages.find(message => message.verb === 'watch');
  assert.equal(firstRequest.args[0], 'session-a');
  page.navigate('session-b');
  const watchRequests = page.socketMessages.filter(message => message.verb === 'watch');
  assert.equal(watchRequests.at(-1).args[0], 'session-b');
  socket.onmessage({ data: JSON.stringify({
    verb: 'watch', noun: 'session', status: 'ok', request_id: firstRequest.request_id,
    data: { watch_id: 'stale-watch', session_id: 'session-a' },
  }) });
  assert.deepEqual(page.socketMessages.at(-1), {
    verb: 'unwatch', noun: 'watch', args: ['stale-watch'],
  });
});

test('timer ticks and push bursts share one chat refresh and leave hidden dashboard alone', async () => {
  const page = browser();
  page.navigate('session');
  const release = page.blockNext();
  page.startChatPoll();
  const socket = page.openSocket();
  const read = page.loadChat();
  const tick = page.tick(3000);
  for (let i = 0; i < 10; i++) {
    socket.onmessage({ data: JSON.stringify({ verb: 'refresh', noun: 'dashboard' }) });
  }
  assert.equal(page.calls.length, 1);
  release();
  await Promise.all([read, tick]);
  assert.equal(page.rendered.length, 1);
});

test('dashboard refreshes are single-flight and recover after a failed request', async () => {
  const page = browser();
  const release = page.blockNext();
  const first = page.loadDashboard();
  const second = page.loadDashboard();
  assert.equal(page.calls.length, 1);
  release();
  await Promise.all([first, second]);
  page.failNext();
  await page.loadDashboard();
  await page.loadDashboard();
  assert.equal(page.calls.length, 3);
});

test('dashboard keeps the last conversation list during connection loss', async () => {
  const page = browser();
  page.seedDashboard();
  const content = page.dashboardHTML();
  page.failNext();
  await page.loadDashboard();
  assert.equal(page.dashboardHTML(), content);
});

test('chat rejects A-to-B-to-A stale results and refreshes the selected generation', async () => {
  const page = browser();
  page.navigate('a');
  page.history('old a');
  const release = page.blockNext();
  const first = page.loadChat();
  page.navigate('b');
  const second = page.loadChat();
  page.navigate('a');
  page.history('new a');
  const third = page.loadChat();
  assert.equal(page.calls.length, 1);
  release();
  await Promise.all([first, second, third]);
  assert.equal(page.calls.length, 2);
  assert.equal(page.rendered.length, 1);
  assert.equal(page.rendered[0].conversation[0].content, 'new a');
});

test('optional-panel timeout retains last good activity and failed history retries next tick', async () => {
  const page = browser();
  page.navigate('session');
  page.cachePanels();
  page.history('initial');
  page.startChatPoll();
  await page.loadChat();
  page.startChatPoll();
  page.failNext();
  await page.tick(3000);
  assert.equal(page.rendered.length, 1);
  page.history('recovered');
  await page.tick(3000);
  assert.equal(page.rendered.at(-1).conversation[0].content, 'initial');
  const panels = page.panels();
  assert.equal(panels.currentSessionStatus, 'working');
  assert.equal(panels.lastActivity[0].summary, 'last good activity');
  assert.equal(panels.lastSchedules.length, 1);
  assert.equal(panels.allSubagents.length, 1);
});
