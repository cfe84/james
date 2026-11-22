self.addEventListener('activate', event => event.waitUntil(self.clients.claim()));

self.addEventListener('push', event => {
  if (!event.data) return;
  const { sessionId, kind } = event.data.json() || {};
  if (!/^[0-9a-f-]{36}$/.test(sessionId) || !['attention', 'result'].includes(kind)) return;
  event.waitUntil(self.registration.showNotification('Qew', {
    body: kind === 'attention' ? 'An agent needs your attention.' : 'An agent has a result to review.',
    icon: '/qew-192.png',
    tag: `qew-${kind}-${sessionId}`,
    data: { route: `#/session/${sessionId}` },
  }));
});

self.addEventListener('notificationclick', event => {
  event.notification.close();
  const route = event.notification.data?.route;
  if (!/^#\/session\/[0-9a-f-]{36}$/.test(route)) return;
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: 'window' });
    const app = windows.find(client => new URL(client.url).pathname === '/');
    if (app) {
      app.postMessage({ route });
      await app.focus();
    } else {
      await self.clients.openWindow('/' + route);
    }
  })());
});
