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
  let currentSessionMP = '';
  let queuedMessages = []; // optimistic messages not yet confirmed by server
  let lastSessionStates = {}; // track WORKING→READY transitions for notifications
  let soundEnabled = true;
  let projectFilter = ''; // current project filter
  let projectsCache = []; // cached project list

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

  // --- Projects ---

  async function loadProjects() {
    try {
      const resp = await apiCall('list', 'project', []);
      if (resp.status === 'ok' && resp.data && resp.data.rows) {
        projectsCache = resp.data.rows.map(r => ({
          id: r[0], name: r[1], status: r[2], moneypenny: r[3], agent: r[4], paths: r[5],
        }));
      }
    } catch (e) { /* ignore */ }
    updateProjectFilter();
  }

  function updateProjectFilter() {
    const select = document.getElementById('project-filter');
    const current = select.value;
    select.innerHTML = '<option value="">All sessions</option>';
    for (const p of projectsCache) {
      if (p.status === 'done') continue;
      const opt = document.createElement('option');
      opt.value = p.name;
      opt.textContent = p.name;
      select.appendChild(opt);
    }
    select.value = current || '';
  }

  // --- Dashboard ---

  async function loadDashboard() {
    try {
      const args = ['--all'];
      if (projectFilter) args.push('--project', projectFilter);
      const resp = await apiCall('dashboard', '', args);
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
        <div class="session-row" data-session-id="${escapeAttr(e.sessionId)}" data-session-name="${escapeAttr(e.name || e.sessionId.substring(0, 12))}" data-mp="${escapeAttr(e.moneypenny)}">
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
        openChat(row.dataset.sessionId, row.dataset.sessionName, row.dataset.mp);
      });
    });
  }

  // --- Chat ---

  async function openChat(sessionId, name, mp) {
    currentSession = sessionId;
    currentSessionMP = mp || '';
    document.getElementById('dashboard-view').style.display = 'none';
    document.getElementById('chat-view').style.display = 'flex';
    document.getElementById('chat-title').textContent = name || sessionId.substring(0, 12);
    document.getElementById('chat-mp').textContent = currentSessionMP ? '@ ' + currentSessionMP : '';
    document.getElementById('chat-messages').innerHTML = '<div class="loading">Loading...</div>';
    document.getElementById('chat-input').value = '';
    lastChatHTML = '';
    queuedMessages = [];
    await loadChat();
    startChatPoll();
  }

  function closeChat() {
    currentSession = null;
    currentSessionMP = '';
    document.getElementById('chat-view').style.display = 'none';
    document.getElementById('dashboard-view').style.display = 'flex';
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
      // Extract session status and moneypenny.
      currentSessionStatus = '';
      if (showResp && showResp.status === 'ok' && showResp.data) {
        currentSessionStatus = showResp.data.status || '';
        if (showResp.data.moneypenny && !currentSessionMP) {
          currentSessionMP = showResp.data.moneypenny;
          document.getElementById('chat-mp').textContent = '@ ' + currentSessionMP;
        }
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
    if (!data) {
      container.innerHTML = '<div class="empty-state">No data received</div>';
      return;
    }
    if (!data.conversation || data.conversation.length === 0) {
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

  // --- Create Session Wizard ---

  let wizardState = { step: 1, moneypennies: [], selectedMP: '', currentPath: '', projects: [] };

  async function openCreateWizard() {
    wizardState = { step: 1, moneypennies: [], selectedMP: '', currentPath: '', projects: projectsCache };
    showWizardStep1();
  }

  function closeWizard() {
    const overlay = document.querySelector('.modal-overlay');
    if (overlay) overlay.remove();
  }

  async function showWizardStep1() {
    // Load moneypennies.
    try {
      const resp = await apiCall('list', 'moneypenny', []);
      if (resp.status === 'ok' && resp.data && resp.data.rows) {
        wizardState.moneypennies = resp.data.rows.map(r => ({
          name: r[0], type: r[1], address: r[2], isDefault: r[3] === '*',
        }));
      }
    } catch (e) { /* ignore */ }

    if (wizardState.moneypennies.length === 0) {
      alert('No moneypennies registered. Add one first via the TUI or CLI.');
      return;
    }

    // Auto-select default.
    const def = wizardState.moneypennies.find(m => m.isDefault);
    if (def) wizardState.selectedMP = def.name;
    else wizardState.selectedMP = wizardState.moneypennies[0].name;

    // If only one moneypenny, skip to step 2.
    if (wizardState.moneypennies.length === 1) {
      showWizardStep2();
      return;
    }

    renderWizardModal(`
      <h3>New Session</h3>
      <div class="step-label">Step 1 of 3 — Select Moneypenny</div>
      ${wizardState.moneypennies.map(m => `
        <div class="dir-entry${m.name === wizardState.selectedMP ? ' selected' : ''}" data-mp="${escapeAttr(m.name)}">
          📡 ${escapeHtml(m.name)} <span style="color:var(--muted);font-size:0.85em">(${escapeHtml(m.type)})</span>
          ${m.isDefault ? '<span style="color:var(--primary);font-size:0.8em;margin-left:4px">default</span>' : ''}
        </div>
      `).join('')}
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" onclick="window._qewWizardStep2()">Next</button>
      </div>
    `);

    document.querySelectorAll('.modal .dir-entry').forEach(el => {
      el.addEventListener('click', () => {
        document.querySelectorAll('.modal .dir-entry').forEach(e => e.classList.remove('selected'));
        el.classList.add('selected');
        wizardState.selectedMP = el.dataset.mp;
      });
    });
  }

  async function showWizardStep2() {
    if (!wizardState.currentPath) wizardState.currentPath = '~';
    await renderPathBrowser();
  }

  async function renderPathBrowser() {
    renderWizardModal(`
      <h3>New Session</h3>
      <div class="step-label">Step 2 of 3 — Select Path</div>
      <div class="dir-current">📂 ${escapeHtml(wizardState.currentPath)}</div>
      <div class="dir-browser" id="wizard-dirs"><div class="loading">Loading...</div></div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewWizardBack()">Back</button>
        <button class="btn" onclick="window._qewWizardStep3()">Use this path</button>
      </div>
    `);

    try {
      const resp = await apiCall('list-directory', '', ['-m', wizardState.selectedMP, '--path', wizardState.currentPath]);
      if (resp.status === 'ok' && resp.data) {
        wizardState.currentPath = resp.data.path || wizardState.currentPath;
        document.querySelector('.dir-current').textContent = '📂 ' + wizardState.currentPath;
        const entries = (resp.data.entries || []).filter(e => e.is_dir);
        let html = '';
        html += `<div class="dir-entry" data-path="..">📁 ..</div>`;
        for (const entry of entries) {
          html += `<div class="dir-entry" data-path="${escapeAttr(entry.name)}">📁 ${escapeHtml(entry.name)}</div>`;
        }
        document.getElementById('wizard-dirs').innerHTML = html;
        document.querySelectorAll('#wizard-dirs .dir-entry').forEach(el => {
          el.addEventListener('click', () => {
            const name = el.dataset.path;
            if (name === '..') {
              const parts = wizardState.currentPath.split('/');
              parts.pop();
              wizardState.currentPath = parts.join('/') || '/';
            } else {
              wizardState.currentPath = wizardState.currentPath.replace(/\/$/, '') + '/' + name;
            }
            renderPathBrowser();
          });
        });
      } else {
        document.getElementById('wizard-dirs').innerHTML = '<div class="empty-state">Could not list directory</div>';
      }
    } catch (e) {
      document.getElementById('wizard-dirs').innerHTML = `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  function showWizardStep3() {
    const projectOpts = wizardState.projects.filter(p => p.status !== 'done')
      .map(p => `<option value="${escapeAttr(p.name)}">${escapeHtml(p.name)}</option>`).join('');

    renderWizardModal(`
      <h3>New Session</h3>
      <div class="step-label">Step 3 of 3 — Session Details</div>
      <div style="font-size:0.85em;color:var(--muted);margin-bottom:8px">
        📡 ${escapeHtml(wizardState.selectedMP)} &nbsp; 📂 ${escapeHtml(wizardState.currentPath)}
      </div>
      <label for="wiz-prompt">Prompt *</label>
      <textarea id="wiz-prompt" rows="3" placeholder="What should the agent do?"></textarea>
      <label for="wiz-name">Name (optional)</label>
      <input id="wiz-name" type="text" placeholder="Auto-generated from prompt">
      <label for="wiz-project">Project</label>
      <select id="wiz-project"><option value="">(none)</option>${projectOpts}</select>
      <label for="wiz-agent">Agent</label>
      <input id="wiz-agent" type="text" placeholder="claude" value="claude">
      <label for="wiz-sysprompt">System Prompt (optional)</label>
      <textarea id="wiz-sysprompt" rows="2" placeholder=""></textarea>
      <div class="toggle-row">
        <input type="checkbox" id="wiz-yolo">
        <label for="wiz-yolo" style="margin:0;color:var(--text)">Yolo mode (skip permission prompts)</label>
      </div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewWizardBackToPath()">Back</button>
        <button class="btn" id="wiz-submit">Create Session</button>
      </div>
    `);

    // If a project is pre-selected from the filter, select it.
    if (projectFilter) {
      document.getElementById('wiz-project').value = projectFilter;
    }

    document.getElementById('wiz-submit').addEventListener('click', submitCreateSession);
  }

  async function submitCreateSession() {
    const prompt = document.getElementById('wiz-prompt').value.trim();
    if (!prompt) {
      alert('Prompt is required');
      return;
    }

    const args = ['-m', wizardState.selectedMP, '--path', wizardState.currentPath, '--async'];
    const name = document.getElementById('wiz-name').value.trim();
    if (name) args.push('--name', name);
    const project = document.getElementById('wiz-project').value;
    if (project) args.push('--project', project);
    const agent = document.getElementById('wiz-agent').value.trim();
    if (agent) args.push('--agent', agent);
    const sysprompt = document.getElementById('wiz-sysprompt').value.trim();
    if (sysprompt) args.push('--system-prompt', sysprompt);
    if (document.getElementById('wiz-yolo').checked) args.push('--yolo');
    args.push(prompt);

    document.getElementById('wiz-submit').disabled = true;
    document.getElementById('wiz-submit').textContent = 'Creating...';

    try {
      const resp = await apiCall('create', 'session', args);
      if (resp.status === 'error') {
        alert('Error: ' + resp.message);
        document.getElementById('wiz-submit').disabled = false;
        document.getElementById('wiz-submit').textContent = 'Create Session';
        return;
      }
      closeWizard();
      await loadDashboard();
    } catch (e) {
      alert('Error: ' + e.message);
      document.getElementById('wiz-submit').disabled = false;
      document.getElementById('wiz-submit').textContent = 'Create Session';
    }
  }

  function renderWizardModal(content) {
    let overlay = document.querySelector('.modal-overlay');
    if (!overlay) {
      overlay = document.createElement('div');
      overlay.className = 'modal-overlay';
      overlay.addEventListener('click', (e) => { if (e.target === overlay) closeWizard(); });
      document.body.appendChild(overlay);
    }
    overlay.innerHTML = `<div class="modal">${content}</div>`;
  }

  // Expose wizard functions for inline onclick handlers.
  window._qewCloseWizard = closeWizard;
  window._qewWizardStep2 = showWizardStep2;
  window._qewWizardStep3 = showWizardStep3;
  window._qewWizardBack = function() {
    if (wizardState.moneypennies.length === 1) { closeWizard(); return; }
    showWizardStep1();
  };
  window._qewWizardBackToPath = showWizardStep2;

  // --- Git Actions ---

  async function showDiff() {
    if (!currentSession) return;
    renderWizardModal(`
      <h3>Git Diff</h3>
      <div class="diff-content"><div class="loading">Loading diff...</div></div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Close</button>
        <button class="btn" onclick="window._qewCommitFromDiff()">Commit</button>
        <button class="btn" onclick="window._qewCommitAndPush()">Commit & Push</button>
      </div>
    `);

    try {
      const resp = await apiCall('diff', 'session', [currentSession]);
      const diffContainer = document.querySelector('.diff-content');
      if (resp.status === 'error') {
        diffContainer.innerHTML = `<span style="color:var(--danger)">${escapeHtml(resp.message)}</span>`;
        return;
      }
      const diffText = resp.data && resp.data.message ? resp.data.message : '';
      if (!diffText) {
        diffContainer.innerHTML = '<span style="color:var(--muted)">No changes (working tree clean)</span>';
        return;
      }
      diffContainer.innerHTML = formatDiff(diffText);
    } catch (e) {
      document.querySelector('.diff-content').innerHTML =
        `<span style="color:var(--danger)">Error: ${escapeHtml(e.message)}</span>`;
    }
  }

  function formatDiff(text) {
    return text.split('\n').map(line => {
      const escaped = escapeHtml(line);
      if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('diff ')) {
        return `<span class="diff-header">${escaped}</span>`;
      }
      if (line.startsWith('@@')) return `<span class="diff-hunk">${escaped}</span>`;
      if (line.startsWith('+')) return `<span class="diff-add">${escaped}</span>`;
      if (line.startsWith('-')) return `<span class="diff-del">${escaped}</span>`;
      return escaped;
    }).join('\n');
  }

  let gitPushAfterCommit = false;

  function showCommitModal(pushAfter) {
    gitPushAfterCommit = !!pushAfter;
    renderWizardModal(`
      <h3>${gitPushAfterCommit ? 'Commit & Push' : 'Git Commit'}</h3>
      <label for="git-commit-msg">Commit message</label>
      <textarea id="git-commit-msg" rows="3" placeholder="Describe your changes..."></textarea>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" id="git-commit-submit">${gitPushAfterCommit ? 'Commit & Push' : 'Commit'}</button>
      </div>
    `);
    document.getElementById('git-commit-msg').focus();
    document.getElementById('git-commit-submit').addEventListener('click', submitCommit);
  }

  async function submitCommit() {
    const msg = document.getElementById('git-commit-msg').value.trim();
    if (!msg) { alert('Commit message is required'); return; }
    if (!currentSession) return;

    const btn = document.getElementById('git-commit-submit');
    btn.disabled = true;
    btn.textContent = 'Committing...';

    try {
      const resp = await apiCall('commit', 'session', [currentSession, '-m', msg]);
      if (resp.status === 'error') {
        alert('Commit error: ' + resp.message);
        btn.disabled = false;
        btn.textContent = gitPushAfterCommit ? 'Commit & Push' : 'Commit';
        return;
      }
      if (gitPushAfterCommit) {
        btn.textContent = 'Pushing...';
        const pushResp = await apiCall('push', 'session', [currentSession]);
        if (pushResp.status === 'error') {
          showGitResult('Committed but push failed: ' + pushResp.message);
          return;
        }
        const pushOutput = pushResp.data && pushResp.data.message ? pushResp.data.message : 'Push complete';
        const commitOutput = resp.data && resp.data.message ? resp.data.message : '';
        showGitResult(commitOutput + '\n' + pushOutput);
      } else {
        showGitResult(resp.data && resp.data.message ? resp.data.message : 'Committed');
      }
    } catch (e) {
      alert('Error: ' + e.message);
      btn.disabled = false;
      btn.textContent = gitPushAfterCommit ? 'Commit & Push' : 'Commit';
    }
  }

  function showBranchModal() {
    if (!currentSession) return;
    renderWizardModal(`
      <h3>Git Branch</h3>
      <label for="git-branch-name">Branch name</label>
      <input id="git-branch-name" type="text" placeholder="feature/my-branch">
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" id="git-branch-submit">Create & Switch</button>
      </div>
    `);
    document.getElementById('git-branch-name').focus();
    document.getElementById('git-branch-submit').addEventListener('click', async () => {
      const name = document.getElementById('git-branch-name').value.trim();
      if (!name) { alert('Branch name is required'); return; }
      const btn = document.getElementById('git-branch-submit');
      btn.disabled = true;
      btn.textContent = 'Creating...';
      try {
        const resp = await apiCall('branch', 'session', [currentSession, '--name', name]);
        if (resp.status === 'error') {
          alert('Branch error: ' + resp.message);
          btn.disabled = false;
          btn.textContent = 'Create & Switch';
          return;
        }
        showGitResult(resp.data && resp.data.message ? resp.data.message : 'Branch created');
      } catch (e) {
        alert('Error: ' + e.message);
        btn.disabled = false;
        btn.textContent = 'Create & Switch';
      }
    });
  }

  async function gitPush() {
    if (!currentSession) return;
    renderWizardModal(`
      <h3>Git Push</h3>
      <div class="loading">Pushing...</div>
    `);
    try {
      const resp = await apiCall('push', 'session', [currentSession]);
      if (resp.status === 'error') {
        showGitResult('Push failed: ' + resp.message);
        return;
      }
      showGitResult(resp.data && resp.data.message ? resp.data.message : 'Push complete');
    } catch (e) {
      showGitResult('Error: ' + e.message);
    }
  }

  function showGitResult(output) {
    renderWizardModal(`
      <h3>Result</h3>
      <div class="git-output">${escapeHtml(output)}</div>
      <div class="modal-actions">
        <button class="btn" onclick="window._qewCloseWizard()">OK</button>
      </div>
    `);
  }

  window._qewCommitFromDiff = function() { showCommitModal(false); };
  window._qewCommitAndPush = function() { showCommitModal(true); };

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
    // Browser notification (works when tab is in background).
    if (Notification.permission === 'granted') {
      new Notification('Session ready', { body: sessionName });
    } else if (Notification.permission !== 'denied') {
      Notification.requestPermission();
    }
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
  document.getElementById('new-session-btn').addEventListener('click', openCreateWizard);
  document.getElementById('chat-diff').addEventListener('click', showDiff);
  document.getElementById('chat-commit').addEventListener('click', () => showCommitModal(false));
  document.getElementById('chat-branch').addEventListener('click', showBranchModal);
  document.getElementById('chat-push').addEventListener('click', gitPush);
  document.getElementById('project-filter').addEventListener('change', (e) => {
    projectFilter = e.target.value;
    loadDashboard();
  });

  const chatInput = document.getElementById('chat-input');
  chatInput.addEventListener('keydown', e => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      sendMessage();
    }
  });
  chatInput.addEventListener('input', () => autoResize(chatInput));

  // Initial load.
  Promise.all([loadProjects(), loadDashboard()]).then(() => {
    document.getElementById('conn-status').innerHTML =
      '<span class="status-dot connected"></span>Connected';
    startDashboardPoll();
  }).catch(() => {
    document.getElementById('conn-status').innerHTML =
      '<span class="status-dot disconnected"></span>Disconnected';
  });
})();
