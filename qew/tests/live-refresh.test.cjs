const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const app = fs.readFileSync(process.env.QEW_APP_SOURCE ||
  path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');

// Run the application's refresh and socket handlers, substituting only browser
// services, API responses and rendering so timer behavior is deterministic.
function browser() {
  const intervals = new Map();
  const timeouts = new Map();
  const calls = [];
  const rendered = [];
  const sockets = [];
  let timerID = 0;
  let history = { conversation: [], total: 0 };
  let pending = null;
  let rejectNext = false;
  const elements = new Map();
  const context = vm.createContext({
    localStorage: { getItem: () => null },
    window: { location: { protocol: 'https:', host: 'qew.test' } },
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
      send() {}
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
        ? { status: 'ok', data: history } : { status: 'ok', data: { rows: [] } };
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
  const state = app.slice(app.indexOf("'use strict';"), app.indexOf('  function setConnectionState'));
  const dashboard = app.slice(app.indexOf('  // --- Dashboard ---'), app.indexOf('  function renderDashboard'));
  const chat = app.slice(app.search(/^  (?:async )?function loadChat\(/m), app.indexOf('  // mergeRecentHistory'));
  const polling = app.slice(app.indexOf('  // --- Polling ---'), app.indexOf('  // --- Helpers ---'));
  vm.runInContext(`${state}\n${dashboard}\n${chat}\n${polling}
    globalThis.controls = {
      loadChat, loadDashboard, connectPushStream, startChatPoll, startDashboardPoll,
      navigate(id) {
        currentSession = id;
        if (typeof chatGeneration !== 'undefined') chatGeneration++;
      },
      cachePanels() {
        currentSessionStatus = 'working';
        lastActivity = [{ summary: 'last good activity' }];
        lastSchedules = [{ id: 1 }];
        allSubagents = [{ sessionId: 'child', status: 'working' }];
      },
      panels() { return { currentSessionStatus, lastActivity, lastSchedules, allSubagents }; }
    };`, context);
  return {
    ...context.controls, calls, rendered, sockets,
    history(content) { history = { conversation: [{ role: 'assistant', content }], total: 1 }; },
    blockNext() { let release; pending = new Promise(resolve => { release = resolve; }); return release; },
    failNext() { rejectNext = true; },
    async tick(ms) {
      for (const timer of [...intervals.values()].filter(timer => timer.ms === ms)) await timer.fn();
    },
    timers(ms) { return [...intervals.values()].filter(timer => timer.ms === ms).length; },
    openSocket() {
      context.controls.connectPushStream();
      const socket = sockets.at(-1);
      socket.readyState = 1;
      socket.onopen();
      return socket;
    },
  };
}

for (const withSocket of [false, true]) {
  test(`chat advances without hints (${withSocket ? 'healthy WebSocket' : 'no push backend'})`, async () => {
    const page = browser();
    page.navigate('session');
    page.history('initial');
    page.startChatPoll();
    if (withSocket) page.openSocket();
    await page.loadChat();
    page.history('new reply');
    await page.tick(3000);
    assert.equal(page.rendered.at(-1).conversation[0].content, 'new reply');
    assert.equal(page.timers(3000), 1);
    assert.equal(page.timers(5000), 0);
  });
}

test('healthy socket retains dashboard polling and reconnect does not duplicate timers', async () => {
  const page = browser();
  page.startDashboardPoll();
  const socket = page.openSocket();
  await page.loadDashboard();
  const before = page.calls.length;
  await page.tick(5000);
  assert.equal(page.calls.length, before + 1);
  socket.close();
  page.openSocket();
  await page.loadDashboard();
  assert.equal(page.timers(5000), 1);
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
  page.failNext();
  await page.tick(3000);
  assert.equal(page.rendered.length, 1);
  page.history('recovered');
  await page.tick(3000);
  assert.equal(page.rendered.at(-1).conversation[0].content, 'recovered');
  const panels = page.panels();
  assert.equal(panels.currentSessionStatus, 'working');
  assert.equal(panels.lastActivity[0].summary, 'last good activity');
  assert.equal(panels.lastSchedules.length, 1);
  assert.equal(panels.allSubagents.length, 1);
});
