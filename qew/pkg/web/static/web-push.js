(() => {
  const button = document.getElementById('push-toggle');
  if (!window.isSecureContext || !('serviceWorker' in navigator) ||
      !('PushManager' in window) || !('Notification' in window)) return;

  function setEnabled(enabled) {
    button.setAttribute('aria-pressed', String(enabled));
    button.title = enabled ? 'Disable background alerts' : 'Enable background alerts';
  }

  async function request(method, subscription) {
    const response = await fetch('/web-push', {
      method, headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'QewClient' },
      body: subscription ? JSON.stringify(subscription) : undefined,
    });
    if (response.redirected) throw new Error('Sign in again to change background alerts.');
    if (!response.ok) throw new Error((await response.text()).trim());
    return method === 'GET' ? response.json() : undefined;
  }

  async function initialize() {
    const { publicKey } = await request('GET');
    const bytes = Uint8Array.from(atob(publicKey.replace(/-/g, '+').replace(/_/g, '/')), c => c.charCodeAt(0));
    const matches = subscription => {
      const key = new Uint8Array(subscription?.options.applicationServerKey);
      return key.length === bytes.length && bytes.every((byte, i) => byte === key[i]);
    };
    const registration = await navigator.serviceWorker.getRegistration('/');
    const existing = await registration?.pushManager.getSubscription();
    setEnabled(Notification.permission === 'granted' && matches(existing));
    button.hidden = false;
    button.addEventListener('click', async () => {
      button.disabled = true;
      const enabled = button.getAttribute('aria-pressed') === 'true';
      try {
        if (!enabled && await Notification.requestPermission() !== 'granted') {
          throw new Error('Allow notifications for Qew in Chrome to receive background alerts.');
        }
        await navigator.serviceWorker.register('/qew-sw.js');
        const worker = await navigator.serviceWorker.ready;
        let subscription = await worker.pushManager.getSubscription();
        if (enabled && subscription) {
          await request('DELETE', subscription);
          setEnabled(false);
          await subscription.unsubscribe();
        } else if (!enabled) {
          if (subscription && !matches(subscription)) {
            await subscription.unsubscribe();
            subscription = null;
          }
          subscription ??= await worker.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: bytes });
          try {
            await request('POST', subscription);
          } catch (error) {
            await subscription.unsubscribe().catch(cleanupError =>
              console.warn('Could not remove the unregistered push subscription', cleanupError));
            throw error;
          }
          setEnabled(true);
        } else {
          setEnabled(false);
        }
      } catch (error) {
        window.alert(error.message);
      } finally {
        button.disabled = false;
      }
    });
  }

  navigator.serviceWorker.addEventListener('message', event => {
    if (event.source === navigator.serviceWorker.controller &&
        /^#\/session\/[0-9a-f-]{36}$/.test(event.data?.route)) {
      location.hash = event.data.route;
    }
  });
  initialize().catch(error => console.warn('Background alerts are unavailable:', error.message));
})();
