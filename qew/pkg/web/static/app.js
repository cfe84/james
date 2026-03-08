(function() {
  'use strict';

  const POLL_INTERVAL = 5000;
  let ws = null;
  let currentSession = null;
  let pollTimer = null;
  let chatPollTimer = null;
  let requestQueue = [];
  let requestId = 0;
  let lastChatHTML = '';
  let currentSessionStatus = '';
  let queuedMessages = []; // optimistic messages not yet confirmed by server
  let lastSessionStates = {}; // track WORKING→READY transitions for notifications
  let soundEnabled = true;

  // --- API ---

  async function apiCall(verb, noun, args) {
    const resp = await fetch('/api', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'QewClient' },
      body: JSON.stringify({ verb, noun, args: args || [] }),
    });
    if (resp.status === 401 || resp.status === 302) {
      window.location.href = '/login';
      throw new Error('Session expired');
    }
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return resp.json();
  }

  // --- Dashboard ---

  async function loadDashboard() {
    try {
      const resp = await apiCall('dashboard', '', ['--all']);
      if (resp.status === 'error') {
        document.getElementById('dash-content').innerHTML =
          `<div class="empty-state">Error: ${escapeHtml(resp.message)}</div>`;
        return;
      }
      // Detect WORKING→READY transitions for notifications.
      if (resp.data && resp.data.rows) {
        for (const row of resp.data.rows) {
          const sessionId = row[0] || '';
          const statusRaw = row[3] || '';
          const idx = statusRaw.indexOf(' (');
          const mpStatus = idx >= 0 ? statusRaw.substring(0, idx) : statusRaw;
          const prev = lastSessionStates[sessionId];
          if (prev === 'working' && mpStatus === 'ready') {
            const name = row[1] || sessionId.substring(0, 12);
            showNotification(name);
          }
          lastSessionStates[sessionId] = mpStatus;
        }
      }
      renderDashboard(resp.data);
    } catch (e) {
      document.getElementById('dash-content').innerHTML =
        `<div class="empty-state">Connection error: ${escapeHtml(e.message)}</div>`;
    } finally {
      document.getElementById('dash-loading').style.display = 'none';
    }
  }

  function renderDashboard(data) {
    const container = document.getElementById('dash-content');
    if (!data || !data.rows || data.rows.length === 0) {
      container.innerHTML = '<div class="empty-state">No sessions</div>';
      return;
    }

    // Parse entries and categorize.
    const entries = data.rows.map(row => {
      const e = {
        sessionId: row[0] || '',
        name: row[1] || '',
        project: row[2] || '',
        statusRaw: row[3] || '',
        moneypenny: row[4] || '',
        lastActive: row[5] || '',
      };
      e.mpStatus = e.statusRaw;
      e.hemStatus = 'active';
      if (e.statusRaw.includes('(completed)')) e.hemStatus = 'completed';
      const idx = e.statusRaw.indexOf(' (');
      if (idx >= 0) e.mpStatus = e.statusRaw.substring(0, idx);

      if (e.hemStatus === 'completed') e.category = 3;
      else if (e.mpStatus === 'ready') e.category = 0;
      else if (e.mpStatus === 'idle' || e.mpStatus === 'offline' || e.mpStatus === 'unknown') e.category = 2;
      else e.category = 1;

      return e;
    });

    // Sort by category, then project, then name.
    entries.sort((a, b) => {
      if (a.category !== b.category) return a.category - b.category;
      if (a.project !== b.project) return a.project.localeCompare(b.project);
      return a.name.localeCompare(b.name);
    });

    const catLabels = [
      { cls: 'cat-ready', label: 'Ready' },
      { cls: 'cat-working', label: 'Working' },
      { cls: 'cat-idle', label: 'Idle' },
      { cls: 'cat-completed', label: 'Completed' },
    ];

    let html = '';
    let lastCat = -1;
    for (const e of entries) {
      if (e.category !== lastCat) {
        if (lastCat !== -1) html += '</div>';
        const cat = catLabels[e.category];
        html += `<div class="category"><span class="category-label ${cat.cls}">${cat.label}</span>`;
        lastCat = e.category;
      }
      const displayName = e.name || e.sessionId.substring(0, 12);
      const statusCls = e.mpStatus === 'working' ? 'working' : (e.mpStatus === 'ready' ? 'ready' : 'idle');
      html += `
        <div class="session-row" data-session-id="${escapeAttr(e.sessionId)}" data-session-name="${escapeAttr(e.name || e.sessionId.substring(0, 12))}">
          <span class="session-name">${escapeHtml(displayName)}</span>
          ${e.project ? `<span class="session-project">${escapeHtml(e.project)}</span>` : ''}
          <span class="session-status ${statusCls}">${escapeHtml(e.mpStatus)}</span>
          <span class="session-mp">${escapeHtml(e.moneypenny)}</span>
        </div>`;
    }
    if (lastCat !== -1) html += '</div>';

    container.innerHTML = html;

    // Click handlers.
    container.querySelectorAll('.session-row').forEach(row => {
      row.addEventListener('click', () => {
        openChat(row.dataset.sessionId, row.dataset.sessionName);
      });
    });
  }

  // --- Chat ---

  async function openChat(sessionId, name) {
    currentSession = sessionId;
    document.getElementById('dashboard-view').style.display = 'none';
    document.getElementById('chat-view').style.display = 'flex';
    document.getElementById('chat-title').textContent = name || sessionId.substring(0, 12);
    document.getElementById('chat-messages').innerHTML = '<div class="loading">Loading...</div>';
    document.getElementById('chat-input').value = '';
    lastChatHTML = '';
    stopDashboardPoll();
    await loadChat();
    startChatPoll();
  }

  function closeChat() {
    currentSession = null;
    document.getElementById('chat-view').style.display = 'none';
    document.getElementById('dashboard-view').style.display = 'block';
    stopChatPoll();
    loadDashboard();
    startDashboardPoll();
  }

  async function loadChat() {
    if (!currentSession) return;
    try {
      const [histResp, showResp, schedResp] = await Promise.all([
        apiCall('history', 'session', [currentSession, '--count', '50']),
        apiCall('show', 'session', [currentSession]).catch(() => null),
        apiCall('list', 'schedule', ['--session-id', currentSession]).catch(() => null),
      ]);
      if (histResp.status === 'error') {
        document.getElementById('chat-messages').innerHTML =
          `<div class="empty-state">Error: ${escapeHtml(histResp.message)}</div>`;
        return;
      }
      // Extract session status.
      currentSessionStatus = '';
      if (showResp && showResp.status === 'ok' && showResp.data) {
        currentSessionStatus = showResp.data.status || '';
      }
      // Extract schedules.
      let schedules = [];
      if (schedResp && schedResp.status === 'ok' && schedResp.data && schedResp.data.rows) {
        schedules = schedResp.data.rows.map(r => ({
          id: r[0], sessionId: r[1], prompt: r[2], scheduledAt: r[3], status: r[4],
        })).filter(s => s.status === 'pending');
      }
      renderChat(histResp.data, schedules);
    } catch (e) {
      document.getElementById('chat-messages').innerHTML =
        `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  function renderChat(data, schedules) {
    const container = document.getElementById('chat-messages');
    if (!data || !data.conversation || data.conversation.length === 0) {
      container.innerHTML = '<div class="empty-state">No messages yet</div>';
      return;
    }

    let html = '';
    // Merge server conversation with queued messages.
    const serverTurns = data.conversation;
    // Remove queued messages that the server now has.
    queuedMessages = queuedMessages.filter(qm => {
      for (const st of serverTurns) {
        if (st.role === 'user' && st.content === qm.content) return false;
      }
      return true;
    });

    for (const turn of serverTurns) {
      const roleLabel = turn.role === 'user' ? '🧑‍💻 you' : (turn.role === 'assistant' ? '🤖 assistant' : '⚙ system');
      const roleClass = turn.role;
      const content = turn.content || '(empty)';
      html += `
        <div class="msg">
          <div class="msg-role ${roleClass}">${roleLabel}${turn.created_at ? ` <span style="color:var(--muted);font-weight:normal">${escapeHtml(turn.created_at)}</span>` : ''}</div>
          <div class="msg-content">${formatContent(content)}</div>
        </div>`;
    }
    // Show queued (optimistic) messages.
    for (const qm of queuedMessages) {
      html += `
        <div class="msg">
          <div class="msg-role user">⏳ you <span style="color:var(--muted);font-weight:normal">[Queued]</span></div>
          <div class="msg-content">${formatContent(qm.content)}</div>
        </div>`;
    }
    // Working indicator.
    if (currentSessionStatus === 'working') {
      html += '<div class="msg working-indicator">🤖 working...</div>';
    }
    // Pending schedules.
    if (schedules && schedules.length > 0) {
      for (const s of schedules) {
        const prompt = s.prompt.length > 80 ? s.prompt.substring(0, 77) + '...' : s.prompt;
        html += `<div class="msg schedule-indicator">⏰ ${escapeHtml(s.scheduledAt)} — ${escapeHtml(prompt)}</div>`;
      }
    }
    // Skip re-render if content hasn't changed (preserves selection and scroll).
    if (html === lastChatHTML) return;
    lastChatHTML = html;
    // Only auto-scroll if user is already near the bottom.
    const atBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 80;
    container.innerHTML = html;
    if (atBottom) container.scrollTop = container.scrollHeight;
  }

  async function sendMessage() {
    const input = document.getElementById('chat-input');
    const text = input.value.trim();
    if (!text || !currentSession) return;

    input.value = '';
    input.style.height = 'auto';
    document.getElementById('chat-send').disabled = true;

    try {
      await apiCall('continue', 'session', [currentSession, '--async', text]);
      // Track as queued and re-render.
      queuedMessages.push({ content: text });
      lastChatHTML = ''; // force re-render
      await loadChat();
    } catch (e) {
      alert('Send error: ' + e.message);
    } finally {
      document.getElementById('chat-send').disabled = false;
    }
  }

  // --- Polling ---

  function startDashboardPoll() {
    stopDashboardPoll();
    pollTimer = setInterval(loadDashboard, POLL_INTERVAL);
  }

  function stopDashboardPoll() {
    if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  }

  function startChatPoll() {
    stopChatPoll();
    chatPollTimer = setInterval(loadChat, 3000);
  }

  function stopChatPoll() {
    if (chatPollTimer) { clearInterval(chatPollTimer); chatPollTimer = null; }
  }

  // --- Helpers ---

  function escapeHtml(s) {
    const div = document.createElement('div');
    div.textContent = s;
    return div.innerHTML;
  }

  function escapeAttr(s) {
    return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')
            .replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/`/g, '&#96;');
  }

  function formatContent(text) {
    // All content is HTML-escaped first, then structural markdown is applied.
    // Since escapeHtml runs first, all user content is safe — regexes only
    // wrap already-escaped text in HTML tags.
    let html = escapeHtml(text);
    // Code blocks: ```...``` (must come first to protect contents from other rules)
    html = html.replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>');
    // Headings: # ## ### #### (must be at start of line)
    html = html.replace(/(^|\n)####\s+(.+)/g, '$1<h4>$2</h4>');
    html = html.replace(/(^|\n)###\s+(.+)/g, '$1<h3>$2</h3>');
    html = html.replace(/(^|\n)##\s+(.+)/g, '$1<h2>$2</h2>');
    html = html.replace(/(^|\n)#\s+(.+)/g, '$1<h1>$2</h1>');
    // Tables: lines of | col | col |
    html = html.replace(/(^|\n)(\|.+\|(?:\n\|.+\|)+)/g, function(match, prefix, table) {
      const rows = table.trim().split('\n');
      let out = '<table>';
      for (let i = 0; i < rows.length; i++) {
        const cells = rows[i].split('|').slice(1, -1);
        if (cells.every(c => /^\s*[-:]+\s*$/.test(c))) continue;
        const tag = i === 0 ? 'th' : 'td';
        // Cell content is already HTML-escaped from the initial escapeHtml call.
        out += '<tr>' + cells.map(c => `<${tag}>${c.trim()}</${tag}>`).join('') + '</tr>';
      }
      out += '</table>';
      return prefix + out;
    });
    // Inline code: `...`
    html = html.replace(/`([^`]+)`/g, '<code>$1</code>');
    // Bold: **...**
    html = html.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    return html;
  }

  // --- Auto-resize textarea ---

  function autoResize(el) {
    el.style.height = 'auto';
    el.style.height = Math.min(el.scrollHeight, 120) + 'px';
  }

  // --- Notifications ---

  function playNotificationSound() {
    if (!soundEnabled) return;
    try {
      const ctx = new (window.AudioContext || window.webkitAudioContext)();
      // Simple two-tone chime.
      [440, 880].forEach((freq, i) => {
        const osc = ctx.createOscillator();
        const gain = ctx.createGain();
        osc.type = 'sine';
        osc.frequency.value = freq;
        gain.gain.setValueAtTime(0.15, ctx.currentTime + i * 0.15);
        gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + i * 0.15 + 0.3);
        osc.connect(gain);
        gain.connect(ctx.destination);
        osc.start(ctx.currentTime + i * 0.15);
        osc.stop(ctx.currentTime + i * 0.15 + 0.3);
      });
    } catch (e) { /* ignore audio errors */ }
  }

  function showNotification(sessionName) {
    playNotificationSound();
    // Pop-over notification.
    const popover = document.createElement('div');
    popover.className = 'notification-popover';
    popover.textContent = 'Session ready: ' + sessionName;
    document.body.appendChild(popover);
    setTimeout(() => popover.classList.add('show'), 10);
    setTimeout(() => {
      popover.classList.remove('show');
      setTimeout(() => popover.remove(), 300);
    }, 4000);
  }

  function toggleSound() {
    soundEnabled = !soundEnabled;
    const btn = document.getElementById('sound-toggle');
    btn.textContent = soundEnabled ? '🔔' : '🔕';
    btn.title = soundEnabled ? 'Sound on (click to mute)' : 'Sound off (click to unmute)';
  }

  // --- Init ---

  document.getElementById('chat-back').addEventListener('click', closeChat);
  document.getElementById('chat-send').addEventListener('click', sendMessage);
  document.getElementById('sound-toggle').addEventListener('click', toggleSound);

  const chatInput = document.getElementById('chat-input');
  chatInput.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendMessage();
    }
  });
  chatInput.addEventListener('input', () => autoResize(chatInput));

  // Initial load.
  loadDashboard().then(() => {
    document.getElementById('conn-status').innerHTML =
      '<span class="status-dot connected"></span>Connected';
    startDashboardPoll();
  }).catch(() => {
    document.getElementById('conn-status').innerHTML =
      '<span class="status-dot disconnected"></span>Disconnected';
  });
})();
