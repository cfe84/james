const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const thresholds = require('../pkg/web/static/compaction-threshold.js');

const app = fs.readFileSync(process.env.QEW_APP_SOURCE ||
  path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');

test('conversation help retains action, compose, focus and scrolling shortcuts', () => {
  const source = app.slice(app.indexOf("  document.addEventListener('keydown', (e) => {"), app.indexOf('  // --- Routing ---'));
  const actions = {
    c: 'complete', u: 'markready', e: 'edit', y: 'duplicate', a: 'subagent',
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
    const source = app.slice(app.indexOf('  function dashAction(cmd)'), app.indexOf('  // --- Dashboard fuzzy filter'));
    vm.runInContext(`${source}\ndashAction('complete');`, context);
    assert.deepEqual(calls, [['selection', expected], ['complete', selected, expected]]);
  }
  assert.match(app, /case 'c': case 'C': e\.preventDefault\(\); dashAction\('complete'\)/);
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

test('dashboard shows the selected model and refreshes the row when it changes', () => {
  const rowElement = {
    dataset: { sessionId: 'session' },
    setAttribute() {},
    addEventListener() {},
  };
  const container = {
    innerHTML: '',
    querySelector() { return this.innerHTML.includes('session-row') ? rowElement : null; },
    querySelectorAll() { return [rowElement]; },
  };
  const context = vm.createContext({
    document: { getElementById: () => container },
    escapeAttr: value => String(value).replaceAll('"', '&quot;'),
    escapeHtml: value => String(value).replaceAll('<', '&lt;'),
    relativeTime: () => '',
    fuzzyMatch: () => true,
    applyDashSelection() {},
    dashFilter: '',
    dashSelectedId: '',
    dashEntries: [],
    dashRowSigs: {},
    lastDashRenderSig: null,
  });
  const source = app.slice(app.indexOf('  function renderDashboard(data)'), app.indexOf('  // Open a dashboard entry'));
  vm.runInContext(`${source}\nglobalThis.render = renderDashboard;`, context);
  const data = model => ({ rows: [['session', 'Arnold', '', 'idle', 'mac', '', '', '', 'opencode', '', model]] });
  context.render(data('azure/gpt-6-luna'));
  assert.match(container.innerHTML, /opencode · azure\/gpt-6-luna/);
  context.render(data('azure/gpt-6-sol'));
  assert.match(rowElement.innerHTML, /opencode · azure\/gpt-6-sol/);
  context.render(data(''));
  assert.match(rowElement.innerHTML, />opencode<\/span>/);
  assert.doesNotMatch(rowElement.innerHTML, /opencode ·/);
  context.render(data('<unsafe>'));
  assert.match(rowElement.innerHTML, /&lt;unsafe>/);
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
function browser({ stableReconcile = false, reconcileRevision = 1, pushFirst = true } = {}) {
  const intervals = new Map();
  const timeouts = new Map();
  const calls = [];
  const rendered = [];
  const sockets = [];
  const socketMessages = [];
  let timerID = 0;
  let history = { conversation: [], total: 0 };
  let pending = null;
  let rejectNext = false;
  const elements = new Map();
  const context = vm.createContext({
    localStorage: { getItem: () => null },
    window: { location: { protocol: 'https:', host: 'qew.test' }, jamesCompactionThreshold: thresholds },
    document: {
      getElementById(id) {
        if (!elements.has(id)) elements.set(id, { style: {}, innerHTML: '' });
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
      const response = verb === 'history'
        ? { status: 'ok', data: history }
        : verb === 'reconcile' && stableReconcile
          ? { status: 'ok', data: { session_id: 'session', total: 1, revision: reconcileRevision, generation: 1 } }
          : { status: 'ok', data: { rows: [] } };
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
    escapeHtml: value => value,
  });
  const source = pushFirst ? app : app.replace('const PUSH_FIRST = true;', 'const PUSH_FIRST = false;');
  const state = source.slice(source.indexOf("'use strict';"), source.indexOf('  function setConnectionState'));
  const ack = source.slice(source.indexOf('  function acknowledgeRenderedSnapshot'), source.indexOf('  // Optional panels'));
  const dashboard = source.slice(source.indexOf('  // --- Dashboard ---'), source.indexOf('  function renderDashboard'));
  const chat = source.slice(source.search(/^  (?:async )?function loadChat\(/m), source.indexOf('  // mergeRecentHistory'));
  const polling = source.slice(source.indexOf('  // --- Polling ---'), source.indexOf('  // --- Helpers ---'));
  vm.runInContext(`${state}\n${ack}\n${dashboard}\n${chat}\n${polling}
    globalThis.controls = {
      loadChat, loadDashboard, connectPushStream, startChatPoll, startDashboardPoll,
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
    ...context.controls, calls, rendered, sockets, socketMessages,
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
