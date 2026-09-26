const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const thresholds = require('../pkg/web/static/compaction-threshold.js');

const app = fs.readFileSync(process.env.QEW_APP_SOURCE ||
  path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');

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
