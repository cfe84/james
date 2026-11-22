const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

test('a background alert reveals no session details and opens its conversation', async () => {
  const handlers = {};
  let shown, opened, existing;
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../pkg/web/static/qew-sw.js'), 'utf8'), {
    URL,
    self: {
      addEventListener: (event, handler) => { handlers[event] = handler; },
      registration: { showNotification: async (title, options) => { shown = { title, options }; } },
      clients: {
        matchAll: async () => existing ? [existing] : [],
        openWindow: async url => { opened = url; },
      },
    },
  });
  function trigger(type, properties) {
    let promise;
    handlers[type]({ ...properties, waitUntil: work => { promise = work; } });
    return promise;
  }
  const sessionId = '12345678-1234-1234-1234-123456789abc';
  await trigger('push', { data: { json: () => ({ sessionId, kind: 'attention', reason: 'Not for the lock screen' }) } });
  assert.equal(shown.title, 'Qew');
  assert.equal(shown.options.body, 'An agent needs your attention.');
  const notification = { close() {}, data: shown.options.data };
  await trigger('notificationclick', { notification });
  assert.equal(opened, `/#/session/${sessionId}`);

  let delivered, focused = false;
  existing = {
    url: 'https://qew.example/#/session/other',
    postMessage: ({ route }) => { delivered = route; },
    focus: async () => { focused = true; },
  };
  opened = undefined;
  await trigger('notificationclick', { notification });
  assert.equal(delivered, `#/session/${sessionId}`);
  assert.equal(focused, true);
  assert.equal(opened, undefined);
  await trigger('notificationclick', { notification: { close() {}, data: { route: '//example.com' } } });
  assert.equal(opened, undefined);
});
