(function() {
  'use strict';

  const POLL_INTERVAL = 5000;
  let ws = null;
  let currentSession = null;
  let pollTimer = null;
  let chatPollTimer = null;
  let requestQueue = [];
  let requestId = 0;

  // --- API ---

  async function apiCall(verb, noun, args) {
    const resp = await fetch('/api', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ verb, noun, args: args || [] }),
    });
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
      const resp = await apiCall('history', 'session', [currentSession, '--count', '50']);
      if (resp.status === 'error') {
        document.getElementById('chat-messages').innerHTML =
          `<div class="empty-state">Error: ${escapeHtml(resp.message)}</div>`;
        return;
      }
      renderChat(resp.data);
    } catch (e) {
      document.getElementById('chat-messages').innerHTML =
        `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  function renderChat(data) {
    const container = document.getElementById('chat-messages');
    if (!data || !data.conversation || data.conversation.length === 0) {
      container.innerHTML = '<div class="empty-state">No messages yet</div>';
      return;
    }

    let html = '';
    for (const turn of data.conversation) {
      const roleLabel = turn.role === 'user' ? '🧑‍💻 you' : (turn.role === 'assistant' ? '🤖 assistant' : '⚙ system');
      const roleClass = turn.role;
      const content = turn.content || '(empty)';
      html += `
        <div class="msg">
          <div class="msg-role ${roleClass}">${roleLabel}${turn.created_at ? ` <span style="color:var(--muted);font-weight:normal">${escapeHtml(turn.created_at)}</span>` : ''}</div>
          <div class="msg-content">${formatContent(content)}</div>
        </div>`;
    }
    container.innerHTML = html;
    container.scrollTop = container.scrollHeight;
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
      // Optimistically add the message.
      const container = document.getElementById('chat-messages');
      container.innerHTML += `
        <div class="msg">
          <div class="msg-role user">🧑‍💻 you</div>
          <div class="msg-content">${formatContent(text)}</div>
        </div>`;
      container.scrollTop = container.scrollHeight;
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
    return s.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function formatContent(text) {
    // Simple markdown-like formatting: code blocks, inline code, bold, tables.
    let html = escapeHtml(text);
    // Code blocks: ```...```
    html = html.replace(/```(\w*)\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>');
    // Tables: lines of | col | col |
    html = html.replace(/(^|\n)(\|.+\|(?:\n\|.+\|)+)/g, function(match, prefix, table) {
      const rows = table.trim().split('\n');
      let out = '<table>';
      for (let i = 0; i < rows.length; i++) {
        const cells = rows[i].split('|').slice(1, -1);
        // Skip separator rows (e.g. |---|---|)
        if (cells.every(c => /^\s*[-:]+\s*$/.test(c))) continue;
        const tag = i === 0 ? 'th' : 'td';
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

  // --- Init ---

  document.getElementById('chat-back').addEventListener('click', closeChat);
  document.getElementById('chat-send').addEventListener('click', sendMessage);

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
