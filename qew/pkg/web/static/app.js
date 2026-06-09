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
  let currentSessionAgent = '';
  let sessionDefaultModel = '';
  let sessionDefaultEffort = '';
  let overrideModel = '';   // temporary per-conversation model override ('' = default)
  let overrideEffort = '';  // temporary per-conversation effort override ('' = default)
  let queuedMessages = []; // optimistic messages not yet confirmed by server
  // Chat history pagination state (mirrors hem/pkg/ui/chat.go). chatConversation
  // holds ONLY server turns (queued messages are tracked separately above).
  let chatConversation = [];   // merged server turns: older (scroll-loaded) + recent (latest poll)
  let chatRecentCount = 0;     // number of turns in the latest poll window
  let chatTotal = 0;           // total server turn count
  let chatLoadingMore = false; // an older-history fetch is in flight
  let chatForceScrollBottom = false; // one-shot: force scroll to bottom on next render
  let lastSchedules = [];      // cached so an older-history re-render needn't refetch
  let lastSubagents = [];
  let lastActivity = [];
  const CHAT_PAGE_SIZE = 50;
  let lastSessionStates = {}; // track WORKING→READY transitions for notifications
  let parentSessionStack = []; // stack for subagent navigation
  let soundEnabled = true;
  // Show persisted train-of-thought (thinking/agent_text) turns. When off, those
  // turns are hidden; live activity for the in-progress turn is still shown while
  // the agent is working. Persisted across reloads.
  let showThoughts = (localStorage.getItem('qewShowThoughts') === '1');
  let projectFilter = ''; // current project filter
  let projectsCache = []; // cached project list
  let dashEntries = [];   // dashboard rows in display order (for keyboard nav)
  let dashSelectedId = ''; // session id of keyboard-selected dashboard row
  let dashFilter = '';     // fuzzy filter term for the session list ('' = no filter)
  let lastDashboardData = null; // most recent dashboard payload (for local re-filter)
  let mgmtCursor = 0;      // selected row index in the active mgmt list (moneypennies/traits)
  let cmdPaletteOpen = false; // the in-conversation command palette modal is open
  let traitsCache = []; // cached trait list [{id,name,preview}]
  let chatInputCache = {}; // sessionId → draft text

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

  // --- Traits ---

  async function loadTraitsCache() {
    try {
      const resp = await apiCall('list', 'trait', []);
      if (resp.status === 'ok' && resp.data && resp.data.rows) {
        traitsCache = resp.data.rows.map(r => ({ id: r[0], name: r[1], preview: r[2], def: r[3] === 'yes' }));
      }
    } catch (e) { /* ignore */ }
  }

  // effortOptions mirrors hem's effortOptions(): the valid --effort values for
  // an agent, with a leading "" entry meaning "no override / default".
  function effortOptions(agent) {
    if (agent === 'copilot') return ['', 'none', 'low', 'medium', 'high', 'xhigh', 'max'];
    return ['', 'low', 'medium', 'high'];
  }

  // effortOptionsHtml builds <option> markup for an effort dropdown.
  function effortOptionsHtml(agent, selected) {
    return effortOptions(agent).map(o => {
      const label = o === '' ? '(default)' : o;
      return `<option value="${escapeAttr(o)}"${o === (selected || '') ? ' selected' : ''}>${escapeHtml(label)}</option>`;
    }).join('');
  }

  // modelOptionsHtml builds <option> markup for a model dropdown. A previously
  // selected model that's no longer in the fetched list is preserved so editing
  // an existing session never silently drops its model.
  function modelOptionsHtml(models, selected) {
    let opts = '<option value="">(default)</option>';
    let found = false;
    for (const m of models) {
      if (m === selected) found = true;
      opts += `<option value="${escapeAttr(m)}"${m === (selected || '') ? ' selected' : ''}>${escapeHtml(m)}</option>`;
    }
    if (selected && !found) {
      opts += `<option value="${escapeAttr(selected)}" selected>${escapeHtml(selected)} (current)</option>`;
    }
    return opts;
  }

  // loadModels fetches the model list for a moneypenny+agent via `list-models`.
  // Returns model value strings (no leading blank). Returns [] on any error so
  // callers fall back to a default-only dropdown.
  async function loadModels(moneypenny, agent) {
    try {
      const args = [];
      if (moneypenny) args.push('-m', moneypenny);
      if (agent) args.push('--agent', agent);
      const resp = await apiCall('list-models', '', args);
      if (resp.status !== 'ok' || !resp.data || !Array.isArray(resp.data.models)) return [];
      return resp.data.models.map(m => m.value || m.name).filter(Boolean);
    } catch (e) { return []; }
  }

  // populateOverrideSelects fills the chat header model/effort dropdowns based on
  // the current session's agent and stored defaults. The first model option is a
  // "Default (...)" entry whose value is '' (meaning no override).
  async function populateOverrideSelects() {
    const modelSel = document.getElementById('chat-model-override');
    const effortSel = document.getElementById('chat-effort-override');
    if (!modelSel || !effortSel) return;
    const agent = currentSessionAgent;
    // Effort options.
    const efOpts = effortOptions(agent);
    effortSel.innerHTML = efOpts.map(o => {
      const label = o === ''
        ? `Default (${sessionDefaultEffort || 'agent default'})`
        : o;
      return `<option value="${escapeAttr(o)}"${o === overrideEffort ? ' selected' : ''}>${escapeHtml(label)}</option>`;
    }).join('');
    // Model options (default entry first).
    const sessAtStart = currentSession;
    const models = await loadModels(currentSessionMP, agent);
    if (currentSession !== sessAtStart) return;
    let opts = `<option value="">Default (${escapeHtml(sessionDefaultModel || 'agent default')})</option>`;
    let found = !overrideModel;
    for (const m of models) {
      if (m === overrideModel) found = true;
      opts += `<option value="${escapeAttr(m)}"${m === overrideModel ? ' selected' : ''}>${escapeHtml(m)}</option>`;
    }
    if (overrideModel && !found) {
      opts += `<option value="${escapeAttr(overrideModel)}" selected>${escapeHtml(overrideModel)}</option>`;
    }
    modelSel.innerHTML = opts;
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
            showNotification(name, sessionId);
          }
          lastSessionStates[sessionId] = mpStatus;
        }
      }
      renderDashboard(resp.data);
    } catch (e) {
      document.getElementById('dash-content').innerHTML =
        `<div class="empty-state">Connection error: ${escapeHtml(e.message)}</div>`;
      dashEntries = [];
      dashSelectedId = '';
    } finally {
      document.getElementById('dash-loading').style.display = 'none';
    }
  }

  function renderDashboard(data) {
    lastDashboardData = data;
    const container = document.getElementById('dash-content');
    if (!data || !data.rows || data.rows.length === 0) {
      container.innerHTML = '<div class="empty-state">No sessions</div>';
      dashEntries = [];
      dashSelectedId = '';
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
        parentSessionId: row[7] || '',
        agent: row[8] || '',
      };
      e.mpStatus = e.statusRaw;
      e.hemStatus = 'active';
      e.subInfo = '';
      if (e.statusRaw.includes('(completed)')) e.hemStatus = 'completed';
      const bracketIdx = e.statusRaw.indexOf(' [');
      if (bracketIdx >= 0) e.subInfo = e.statusRaw.substring(bracketIdx + 1);
      const idx = e.statusRaw.indexOf(' (');
      if (idx >= 0) e.mpStatus = e.statusRaw.substring(0, idx);

      if (e.hemStatus === 'completed') e.category = 3;
      else if (e.mpStatus === 'ready') e.category = 0;
      else if (e.mpStatus === 'idle' || e.mpStatus === 'offline' || e.mpStatus === 'unknown') e.category = 2;
      else e.category = 1;

      return e;
    });

    // Server sends entries pre-sorted with subagents after their parents.

    // Apply the fuzzy filter (if any) against name/project/agent/moneypenny/id.
    const filtered = dashFilter
      ? entries.filter(e => fuzzyMatch(dashFilter, `${e.name} ${e.project} ${e.agent} ${e.moneypenny} ${e.sessionId}`))
      : entries;

    if (filtered.length === 0) {
      container.innerHTML = `<div class="empty-state">No sessions match "${escapeHtml(dashFilter)}"</div>`;
      dashEntries = [];
      applyDashSelection();
      return;
    }

    const catLabels = [
      { cls: 'cat-ready', label: 'Ready' },
      { cls: 'cat-working', label: 'Working' },
      { cls: 'cat-idle', label: 'Idle' },
      { cls: 'cat-completed', label: 'Completed' },
    ];

    let html = '';
    let lastCat = -1;
    for (const e of filtered) {
      if (e.category !== lastCat) {
        if (lastCat !== -1) html += '</div>';
        const cat = catLabels[e.category];
        html += `<div class="category"><span class="category-label ${cat.cls}">${cat.label}</span>`;
        lastCat = e.category;
      }
      const displayName = e.name || e.sessionId.substring(0, 12);
      const statusCls = e.mpStatus === 'working' ? 'working' : (e.mpStatus === 'ready' ? 'ready' : 'idle');
      html += `
        <div class="session-row" data-session-id="${escapeAttr(e.sessionId)}" data-session-name="${escapeAttr(e.name || e.sessionId.substring(0, 12))}" data-mp="${escapeAttr(e.moneypenny)}" data-parent="${escapeAttr(e.parentSessionId)}">
          <span class="session-name">${escapeHtml(displayName)}</span>
          ${e.project ? `<span class="session-project">${escapeHtml(e.project)}</span>` : ''}
          <span class="session-status ${statusCls}">${escapeHtml(e.mpStatus)}${e.subInfo ? ' <span style="opacity:0.7">' + escapeHtml(e.subInfo) + '</span>' : ''}</span>
          ${e.agent ? `<span class="session-agent agent-${escapeAttr(e.agent)}">${escapeHtml(e.agent)}</span>` : ''}
          <span class="session-mp">${escapeHtml(e.moneypenny)}</span>
          ${e.lastActive ? `<span class="session-time">${escapeHtml(relativeTime(e.lastActive))}</span>` : ''}
        </div>`;
    }
    if (lastCat !== -1) html += '</div>';

    container.innerHTML = html;

    // Record rows in display order for keyboard navigation.
    dashEntries = filtered.map(e => ({
      sessionId: e.sessionId,
      name: e.name || e.sessionId.substring(0, 12),
      mp: e.moneypenny,
      parent: e.parentSessionId,
      agent: e.agent,
    }));

    // Click handlers.
    container.querySelectorAll('.session-row').forEach((row, i) => {
      row.addEventListener('click', () => openDashEntry(dashEntries[i]));
    });

    applyDashSelection();
  }

  // Open a dashboard entry, handling subagents (open parent then the sub).
  function openDashEntry(e) {
    if (!e) return;
    if (e.parent) {
      openChat(e.parent, '', e.mp);
      setTimeout(() => window._openSubagent(e.sessionId, e.name), 100);
    } else {
      openChat(e.sessionId, e.name, e.mp);
    }
  }

  // Re-apply the keyboard selection highlight after a dashboard re-render.
  function applyDashSelection() {
    if (!dashSelectedId) return;
    const idx = dashEntries.findIndex(e => e.sessionId === dashSelectedId);
    if (idx < 0) { dashSelectedId = ''; return; }
    const rows = document.querySelectorAll('#dash-content .session-row');
    rows.forEach((r, i) => r.classList.toggle('selected', i === idx));
  }

  // Move the dashboard selection by delta (keyboard nav).
  function dashMove(delta) {
    if (!dashEntries.length) return;
    let idx = dashEntries.findIndex(e => e.sessionId === dashSelectedId);
    if (idx < 0) idx = 0;
    else idx = Math.min(dashEntries.length - 1, Math.max(0, idx + delta));
    dashSelectedId = dashEntries[idx].sessionId;
    const rows = document.querySelectorAll('#dash-content .session-row');
    rows.forEach((r, i) => r.classList.toggle('selected', i === idx));
    if (rows[idx]) rows[idx].scrollIntoView({ block: 'nearest' });
  }

  function dashOpenSelected() {
    const e = dashEntries.find(x => x.sessionId === dashSelectedId);
    if (e) openDashEntry(e);
  }

  // Run a complete/delete/edit action on the keyboard-selected dashboard row.
  // Resolves the entry at call time and no-ops if nothing is selected.
  function dashAction(cmd) {
    const e = dashEntries.find(x => x.sessionId === dashSelectedId);
    if (!e) return;
    if (cmd === 'complete') completeSession(e.sessionId);
    else if (cmd === 'delete') deleteSession(e.sessionId, e.name);
    else if (cmd === 'edit') showEditSessionModal(e.sessionId);
    else if (cmd === 'duplicate') openDuplicateWizard(e.sessionId);
  }

  // --- Dashboard fuzzy filter ---

  // fuzzyMatch returns true when every character of query appears in target in
  // order (case-insensitive subsequence match), the usual lightweight fuzzy
  // filter. An empty query always matches.
  function fuzzyMatch(query, target) {
    const q = query.toLowerCase().replace(/\s+/g, '');
    if (!q) return true;
    const t = target.toLowerCase();
    let qi = 0;
    for (let ti = 0; ti < t.length && qi < q.length; ti++) {
      if (t[ti] === q[qi]) qi++;
    }
    return qi === q.length;
  }

  // Open (reveal + focus) the dashboard filter input.
  function openDashFilter() {
    const input = document.getElementById('dash-filter');
    if (!input) return;
    input.style.display = 'block';
    input.focus();
    input.select();
  }

  // Wire the filter input: live fuzzy filtering, Enter blurs (keeping the filter
  // so the list can be navigated with j/k/Enter), Escape clears + blurs.
  function initDashFilter() {
    const input = document.getElementById('dash-filter');
    if (!input) return;
    input.addEventListener('input', () => {
      dashFilter = input.value;
      // Re-render from the cached dashboard data without a network round-trip.
      if (lastDashboardData) renderDashboard(lastDashboardData);
    });
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        e.stopPropagation();
        input.blur();
        // Select the first result so j/k navigation has an anchor.
        if (dashEntries.length && !dashEntries.some(x => x.sessionId === dashSelectedId)) {
          dashSelectedId = dashEntries[0].sessionId;
          applyDashSelection();
        }
      } else if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        input.value = '';
        dashFilter = '';
        if (lastDashboardData) renderDashboard(lastDashboardData);
        input.blur();
      }
    });
  }

  // --- Management list keyboard navigation (moneypennies / traits views) ---

  // activeMgmtList returns the visible management list ({kind, container}) or
  // null. Moneypennies and traits both render `.mgmt-row` rows; only one of the
  // two views is ever visible at a time.
  function activeMgmtList() {
    const mp = document.getElementById('moneypennies-view');
    if (mp && mp.style.display !== 'none') return { kind: 'mp', container: document.getElementById('mp-content') };
    const tr = document.getElementById('traits-view');
    if (tr && tr.style.display !== 'none') return { kind: 'trait', container: document.getElementById('trait-content') };
    return null;
  }

  function mgmtRows(container) {
    return container ? Array.from(container.querySelectorAll('.mgmt-row')) : [];
  }

  // applyMgmtCursor highlights the row at mgmtCursor (clamped) in the given
  // container; called after each (re)render so the selection survives reloads.
  function applyMgmtCursor(container, scroll) {
    const rows = mgmtRows(container);
    if (!rows.length) { mgmtCursor = 0; return; }
    mgmtCursor = Math.min(rows.length - 1, Math.max(0, mgmtCursor));
    rows.forEach((r, i) => r.classList.toggle('selected', i === mgmtCursor));
    if (scroll && rows[mgmtCursor]) rows[mgmtCursor].scrollIntoView({ block: 'nearest' });
  }

  function mgmtMove(delta) {
    const a = activeMgmtList();
    if (!a) return;
    const rows = mgmtRows(a.container);
    if (!rows.length) return;
    mgmtCursor = Math.min(rows.length - 1, Math.max(0, mgmtCursor + delta));
    applyMgmtCursor(a.container, true);
  }

  // mgmtAction runs the per-view keyboard shortcut for the selected row,
  // mirroring the hem TUI: moneypennies — enter=ping, e=toggle enabled,
  // s=set default, d=delete, n=new; traits — enter/e=edit, d=delete, n=new.
  // Returns true if the key was handled.
  function mgmtAction(key) {
    const a = activeMgmtList();
    if (!a) return false;
    const row = mgmtRows(a.container)[mgmtCursor];
    if (a.kind === 'mp') {
      const btn = row && row.querySelector('button[data-mp]');
      const name = btn ? btn.dataset.mp : null;
      switch (key) {
        case 'Enter': if (name) pingMoneypenny(name); return true;
        case 'e': if (name) toggleMoneypennyEnabled(name); return true;
        case 's': if (name) setDefaultMoneypenny(name); return true;
        case 'd': if (name) deleteMoneypenny(name); return true;
        case 'n': showAddMoneypennyModal(); return true;
      }
    } else {
      const btn = row && row.querySelector('button[data-trait-id]');
      const id = btn ? btn.dataset.traitId : null;
      // The trait name lives on the delete button (the edit button omits it).
      const delBtn = row && row.querySelector('button[data-action="delete"][data-trait-name]');
      const tname = delBtn ? delBtn.dataset.traitName : null;
      switch (key) {
        case 'Enter': case 'e': if (id) showEditTraitModal(id); return true;
        case 'd': if (id) deleteTrait(id, tname); return true;
        case 'n': showCreateTraitModal(); return true;
      }
    }
    return false;
  }

  // handleWizardListKey gives keyboard navigation to the create/duplicate
  // wizard's list steps: the moneypenny picker (`.dir-entry[data-mp]`, j/k move
  // the selection, Enter advances) and the path browser (`.dir-entry[data-path]`,
  // j/k move, Enter opens the directory). Returns true if it handled the key.
  function handleWizardListKey(e) {
    const overlay = document.querySelector('.modal-overlay');
    if (!overlay) return false;
    if (e.ctrlKey || e.metaKey || e.altKey || e.repeat) return false;
    const tag = (e.target.tagName || '').toLowerCase();
    if (tag === 'input' || tag === 'textarea' || tag === 'select' || e.target.isContentEditable) return false;
    const onButton = tag === 'button' || tag === 'a';
    const nav = (entries, onEnter) => {
      let idx = entries.findIndex(el => el.classList.contains('selected'));
      if (e.key === 'ArrowDown' || e.key === 'j') idx = Math.min(entries.length - 1, (idx < 0 ? -1 : idx) + 1);
      else if (e.key === 'ArrowUp' || e.key === 'k') idx = Math.max(0, (idx < 0 ? 0 : idx) - 1);
      else if (e.key === 'Enter') {
        // Let a focused button (Cancel/Back/Next/…) activate natively.
        if (onButton) return false;
        e.preventDefault(); onEnter(idx); return true;
      }
      else return false;
      e.preventDefault();
      entries.forEach((el, i) => el.classList.toggle('selected', i === idx));
      entries[idx].scrollIntoView({ block: 'nearest' });
      return true;
    };
    const mpEntries = Array.from(overlay.querySelectorAll('.dir-entry[data-mp]'));
    if (mpEntries.length) {
      return nav(mpEntries, (idx) => {
        if (idx >= 0) wizardState.selectedMP = mpEntries[idx].dataset.mp;
        showWizardStep2();
      });
    }
    const pathEntries = Array.from(overlay.querySelectorAll('.dir-entry[data-path]'));
    if (pathEntries.length) {
      return nav(pathEntries, (idx) => { if (idx >= 0) pathEntries[idx].click(); });
    }
    return false;
  }

  // Scroll the chat message pane by a half page (dir: -1 up, 1 down).
  function chatScroll(dir) {
    const c = document.getElementById('chat-messages');
    if (c) c.scrollBy({ top: dir * c.clientHeight * 0.5, behavior: 'auto' });
  }

  // --- Chat ---

  let currentSessionName = '';

  async function openChat(sessionId, name, mp) {
    currentSession = sessionId;
    currentSessionName = name || sessionId.substring(0, 12);
    currentSessionMP = mp || '';
    currentSessionAgent = '';
    sessionDefaultModel = '';
    sessionDefaultEffort = '';
    overrideModel = '';
    overrideEffort = '';
    document.getElementById('dashboard-view').style.display = 'none';
    document.getElementById('moneypennies-view').style.display = 'none';
    document.getElementById('projects-view').style.display = 'none';
    document.getElementById('traits-view').style.display = 'none';
    document.getElementById('chat-view').style.display = 'flex';
    document.getElementById('chat-title').textContent = (parentSessionStack.length > 0 ? 'Subagent: ' : '') + currentSessionName;
    document.getElementById('chat-mp').textContent = currentSessionMP ? '@ ' + currentSessionMP : '';
    document.getElementById('chat-context').textContent = '';
    document.getElementById('chat-messages').innerHTML = '<div class="loading">Loading...</div>';
    const chatInput = document.getElementById('chat-input');
    chatInput.value = chatInputCache[sessionId] || '';
    autoResize(chatInput);
    // Focus the message input by default when opening a session.
    chatInput.focus();
    lastChatHTML = '';
    queuedMessages = [];
    chatConversation = [];
    chatRecentCount = 0;
    chatTotal = 0;
    chatLoadingMore = false;
    chatForceScrollBottom = false;
    lastSchedules = [];
    lastSubagents = [];
    lastActivity = [];
    // Update URL hash without triggering hashchange handler.
    const newHash = '#/session/' + sessionId;
    if (window.location.hash !== newHash) {
      history.replaceState(null, '', newHash);
    }
    await loadChat();
    startChatPoll();
  }

  window._openSubagent = function(sessionId, name) {
    // Push current session onto parent stack.
    parentSessionStack.push({ id: currentSession, name: currentSessionName, mp: currentSessionMP });
    openChat(sessionId, name, currentSessionMP);
  };

  function closeChat() {
    // Cache any draft text.
    if (currentSession) {
      const draft = document.getElementById('chat-input').value;
      if (draft) chatInputCache[currentSession] = draft;
      else delete chatInputCache[currentSession];
    }
    // If viewing a subagent, pop back to parent.
    if (parentSessionStack.length > 0) {
      const parent = parentSessionStack.pop();
      openChat(parent.id, parent.name, parent.mp);
      return;
    }
    currentSession = null;
    currentSessionName = '';
    currentSessionMP = '';
    currentSessionAgent = '';
    sessionDefaultModel = '';
    sessionDefaultEffort = '';
    overrideModel = '';
    overrideEffort = '';
    document.getElementById('chat-view').style.display = 'none';
    document.getElementById('dashboard-view').style.display = 'flex';
    if (window.location.hash) history.replaceState(null, '', window.location.pathname);
    stopChatPoll();
    loadDashboard();
    startDashboardPoll();
  }

  async function loadChat() {
    if (!currentSession) return;
    const sessAtStart = currentSession;
    try {
      const calls = [
        apiCall('history', 'session', [currentSession, '--count', String(CHAT_PAGE_SIZE), '--from', '0']),
        apiCall('show', 'session', [currentSession]).catch(() => null),
        apiCall('list', 'schedule', ['--session-id', currentSession]).catch(() => null),
        apiCall('list', 'subsession', [currentSession]).catch(() => null),
      ];
      // Always fetch activity — avoids race where status isn't yet "working" on current poll.
      calls.push(apiCall('activity', 'session', [currentSession]).catch(() => null));
      const [histResp, showResp, schedResp, subsResp, actResp] = await Promise.all(calls);
      // Bail out if the user navigated to a different session while we awaited;
      // otherwise we'd overwrite the new session's state with stale data.
      if (currentSession !== sessAtStart) return;
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
        // Context usage indicator (custom-compaction sessions track this).
        const ctxEl = document.getElementById('chat-context');
        if (ctxEl) {
          const win = showResp.data.context_window || 0;
          const tok = showResp.data.context_tokens || 0;
          if (win > 0) {
            const pct = Math.round(tok / win * 100);
            ctxEl.textContent = `🗃️ ${pct}% (${Math.round(tok/1000)}k/${Math.round(win/1000)}k)`;
            ctxEl.style.color = pct >= 75 ? 'var(--warning, #e0a030)' : 'var(--muted)';
            ctxEl.title = `Context usage: ${tok} / ${win} tokens (${showResp.data.compaction_mode || 'agent'} compaction)`;
          } else {
            ctxEl.textContent = '';
          }
        }
        // Capture agent + default model/effort for the override dropdowns (once).
        if (!currentSessionAgent) {
          currentSessionAgent = showResp.data.agent || '';
          sessionDefaultModel = showResp.data.model || '';
          sessionDefaultEffort = showResp.data.effort || '';
          populateOverrideSelects();
        }
      }
      // Extract schedules.
      let schedules = [];
      if (schedResp && schedResp.status === 'ok' && schedResp.data && schedResp.data.rows) {
        schedules = schedResp.data.rows.map(r => ({
          id: r[0], sessionId: r[1], prompt: r[2], scheduledAt: r[3], status: r[4],
        })).filter(s => s.status === 'pending');
      }
      // Extract subagents.
      let subagents = [];
      if (subsResp && subsResp.status === 'ok' && subsResp.data && subsResp.data.rows) {
        subagents = subsResp.data.rows.map(r => ({
          sessionId: r[0], name: r[1], status: r[2], yolo: r[3] === 'true',
        }));
      }
      // Extract activity.
      let activity = [];
      if (actResp && actResp.status === 'ok' && actResp.data && actResp.data.activity) {
        activity = actResp.data.activity;
      }
      lastSchedules = schedules;
      lastSubagents = subagents;
      lastActivity = activity;
      mergeRecentHistory(histResp.data);
      renderChat(false);
    } catch (e) {
      if (currentSession !== sessAtStart) return;
      document.getElementById('chat-messages').innerHTML =
        `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  // mergeRecentHistory folds the latest poll window (from=0) into chatConversation
  // while preserving any older turns the user scroll-loaded. Mirrors the merge in
  // hem/pkg/ui/chat.go historyLoadedMsg. The "from" offset is end-relative, so as
  // new turns arrive the older/recent boundary shifts by the total delta.
  function mergeRecentHistory(data) {
    const recent = (data && Array.isArray(data.conversation)) ? data.conversation : [];
    const total = (data && typeof data.total === 'number') ? data.total : recent.length;

    // Don't let an empty poll (a transient race during working state) wipe out a
    // conversation we already have — just refresh the known total.
    if (recent.length === 0 && chatConversation.length > 0) {
      chatTotal = total;
      return;
    }

    // While an older-history fetch is in flight, leave chatConversation untouched
    // so the two mutators never interleave on the same array (the older fetch's
    // end-relative offset would otherwise become stale). The next poll reconciles.
    if (chatLoadingMore) {
      chatTotal = total;
      return;
    }

    const previousTotal = chatTotal;
    chatTotal = total;

    // A shrinking total shouldn't happen, but if it does the incremental merge
    // could fabricate/duplicate turns — reset to the recent window instead.
    if (total < previousTotal) {
      chatConversation = recent.slice();
      chatRecentCount = recent.length;
      return;
    }

    const prevServerKnown = chatConversation.length;
    let delta = total - previousTotal;
    if (delta < 0) delta = 0;
    // If more than one page of new turns arrived since the last poll, the recent
    // window (last CHAT_PAGE_SIZE turns) doesn't reach back to the turns we
    // already have — there's an unrecoverable gap between them. Concatenating
    // would create a non-contiguous array and corrupt the end-relative paging
    // offset, so reset to the recent window (the user can scroll-load the rest).
    if (delta > recent.length) {
      chatConversation = recent.slice();
      chatRecentCount = recent.length;
      return;
    }
    let olderCount = prevServerKnown - recent.length + delta;
    if (olderCount < 0) olderCount = 0;
    if (olderCount > prevServerKnown) olderCount = prevServerKnown;
    chatConversation = chatConversation.slice(0, olderCount).concat(recent);
    chatRecentCount = recent.length;
  }

  // recentServerTurns returns the latest poll window (the tail of chatConversation).
  function recentServerTurns() {
    if (chatRecentCount <= 0) return [];
    return chatConversation.slice(chatConversation.length - chatRecentCount);
  }

  // loadOlderHistory fetches the next older page and prepends it. Triggered when
  // the user scrolls to the top of the chat. Mirrors loadOlderHistory in the TUI.
  async function loadOlderHistory() {
    if (chatLoadingMore || !currentSession) return;
    const from = chatConversation.length;
    const remaining = chatTotal - from;
    if (remaining <= 0) return;
    const count = Math.min(CHAT_PAGE_SIZE, remaining);
    chatLoadingMore = true;
    const sessAtStart = currentSession;
    const knownTotal = chatTotal;
    try {
      const resp = await apiCall('history', 'session', [currentSession, '--count', String(count), '--from', String(from)]);
      // Discard if the session changed, or if a concurrent poll moved the total
      // (which would make this end-relative page overlap/misalign). The user can
      // scroll again to retry against the new state.
      if (currentSession !== sessAtStart || chatTotal !== knownTotal) return;
      if (resp.status === 'ok' && resp.data && Array.isArray(resp.data.conversation) && resp.data.conversation.length) {
        chatConversation = resp.data.conversation.concat(chatConversation);
        renderChat(true);
      }
    } catch (e) { /* leave state intact; next scroll retries */ }
    finally {
      chatLoadingMore = false;
    }
  }

  function renderChat(prepend) {
    const container = document.getElementById('chat-messages');
    const serverTurns = chatConversation;
    const schedules = lastSchedules;
    const subagents = lastSubagents;
    const activity = lastActivity;

    // Remove queued messages that the server now has. Match only against the
    // latest poll window (recent turns) — matching the whole loaded history
    // could wrongly drop a freshly-queued message that merely repeats older text.
    const recent = recentServerTurns();
    queuedMessages = queuedMessages.filter(qm => {
      for (const st of recent) {
        if (st.role === 'user' && st.content === qm.content) return false;
      }
      return true;
    });

    const hasIndicators = (currentSessionStatus === 'working') ||
      (schedules && schedules.length > 0) || (subagents && subagents.length > 0);
    if (serverTurns.length === 0 && queuedMessages.length === 0 && !hasIndicators) {
      if (lastChatHTML === '__empty__') return;
      lastChatHTML = '__empty__';
      container.innerHTML = '<div class="empty-state">No messages yet</div>';
      return;
    }

    let html = '';
    let skippedThoughts = false;
    for (const turn of serverTurns) {
      const agentName = currentSessionName || 'agent';
      const content = turn.content || '(empty)';

      // Train-of-thought turns get a compact, indented, gray rendering with
      // the emoji inline — no role-name header. Hidden unless the toggle is on.
      // Scheduled-invocation prompts render the same way (⏰) so they read as
      // background activity rather than a user message.
      if (turn.role === 'thinking' || turn.role === 'agent_text' || turn.role === 'scheduled') {
        if (!showThoughts) { skippedThoughts = true; continue; }
        const icon = turn.role === 'thinking' ? '💭' : turn.role === 'scheduled' ? '⏰' : '📝';
        html += `<div class="msg thought">${icon} ${formatContent(content)}</div>`;
        continue;
      }

      // Compaction marker: collapsed single line. The distillation's
      // thinking/agent_text turns (shown when train-of-thought is on) carry the
      // detail.
      if (turn.role === 'compaction') {
        html += `<div class="msg system-note">🗃️ Session compacted</div>`;
        continue;
      }

      // System turns (e.g. agent process errors) are de-emphasized so they don't
      // read like the agent's final answer.
      if (turn.role === 'system') {
        html += `<div class="msg system-note">⚙ ${formatContent(content)}</div>`;
        continue;
      }

      let roleLabel;
      switch (turn.role) {
        case 'user':       roleLabel = '🧑‍💻 you'; break;
        case 'assistant':  roleLabel = '🕴️ ' + agentName; break;
        default:           roleLabel = turn.role;
      }
      const roleClass = turn.role;
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
    // Working indicator with activity events.
    if (currentSessionStatus === 'working') {
      if (activity && activity.length > 0) {
        const recentAct = activity.slice(-5);
        for (const ev of recentAct) {
          const icon = ev.type === 'tool_use' ? '🔧' : ev.type === 'text' ? '📝' : '💭';
          html += `<div class="msg activity-indicator">${icon} ${escapeHtml(ev.summary)}</div>`;
        }
      } else {
        const spyVerbs = ['Infiltrating...', 'Surveilling...', 'Decrypting...', 'On a mission...', 'Going undercover...', 'Acquiring intel...', 'Intercepting...', 'Extracting...'];
        const spyVerb = spyVerbs[Math.floor(Math.random() * spyVerbs.length)];
        html += `<div class="msg working-indicator">🕴️ ${spyVerb}</div>`;
      }
    }
    // Pending schedules.
    if (schedules && schedules.length > 0) {
      for (const s of schedules) {
        const prompt = s.prompt.length > 80 ? s.prompt.substring(0, 77) + '...' : s.prompt;
        html += `<div class="msg schedule-indicator">⏰ ${escapeHtml(s.scheduledAt)} — ${escapeHtml(prompt)}</div>`;
      }
    }
    // Subagents.
    if (subagents && subagents.length > 0) {
      for (let si = 0; si < subagents.length; si++) {
        const sub = subagents[si];
        const name = sub.name || (sub.sessionId ? sub.sessionId.substring(0, 12) + '...' : '?');
        const num = sub.yolo ? '00' + (si + 1) : String(si + 1);
        html += `<div class="msg subagent-indicator" style="cursor:pointer" onclick="window._openSubagent('${sub.sessionId}','${escapeHtml(name)}')">🕴️ ${num} ${escapeHtml(name)} [${escapeHtml(sub.status)}]</div>`;
      }
    }
    // If every server turn was a hidden train-of-thought turn (and there are no
    // indicators/queued messages), show a hint instead of leaving the pane on
    // its stale/loading content — `html===''` would otherwise match an empty
    // `lastChatHTML` and skip the DOM update below.
    if (html === '') {
      html = skippedThoughts
        ? '<div class="empty-state">Train of thought hidden — press 💭 (or t) to show</div>'
        : '<div class="empty-state">No messages yet</div>';
    }
    // Skip re-render if content hasn't changed (preserves selection and scroll).
    // A forced bottom-scroll (after sending) must still run, so don't skip then.
    if (html === lastChatHTML && !chatForceScrollBottom) return;
    lastChatHTML = html;

    const prevHeight = container.scrollHeight;
    const prevTop = container.scrollTop;
    const atBottom = prevHeight - prevTop - container.clientHeight < 80;
    container.innerHTML = html;

    if (chatForceScrollBottom) {
      // After sending a message, always reveal it at the bottom.
      container.scrollTop = container.scrollHeight;
      chatForceScrollBottom = false;
    } else if (prepend) {
      // Older turns were prepended — keep the viewport on the same content by
      // offsetting the scroll by the height that was added above.
      container.scrollTop = prevTop + (container.scrollHeight - prevHeight);
    } else if (atBottom) {
      container.scrollTop = container.scrollHeight;
    } else {
      // Preserve the user's reading position across polls (setting innerHTML
      // resets scrollTop to 0 otherwise).
      container.scrollTop = prevTop;
    }
  }

  async function sendMessage() {
    const input = document.getElementById('chat-input');
    const text = input.value.trim();
    if (!text || !currentSession) return;

    input.value = '';
    input.style.height = 'auto';
    delete chatInputCache[currentSession];
    document.getElementById('chat-send').disabled = true;

    try {
      const args = [currentSession, '--async'];
      if (overrideModel) args.push('--model', overrideModel);
      if (overrideEffort) args.push('--effort', overrideEffort);
      args.push(text);
      await apiCall('continue', 'session', args);
      // Track as queued and re-render.
      queuedMessages.push({ content: text });
      chatForceScrollBottom = true; // reveal the new message even if scrolled up
      lastChatHTML = ''; // force re-render
      await loadChat();
    } catch (e) {
      alert('Send error: ' + e.message);
    } finally {
      document.getElementById('chat-send').disabled = false;
    }
  }

  // --- Deploy Agent Wizard ---

  let wizardState = { step: 1, moneypennies: [], selectedMP: '', currentPath: '', projects: [], copy: false, source: null, sourceId: '' };

  // wizardTitle reflects whether the wizard is creating a fresh agent or
  // duplicating an existing session.
  function wizardTitle() { return wizardState.copy ? 'Duplicate Agent' : 'New Agent'; }

  async function openCreateWizard() {
    wizardState = { step: 1, moneypennies: [], selectedMP: '', currentPath: '', projects: projectsCache, copy: false, source: null, sourceId: '' };
    if (traitsCache.length === 0) await loadTraitsCache();
    showWizardStep1();
  }

  // openDuplicateWizard reuses the 3-step create wizard in "copy" mode. It loads
  // the source session's details (via `show session`) to prefill the form and
  // pre-select the source's moneypenny/path, then submits via `copy session`
  // (which inherits any field the user leaves untouched).
  async function openDuplicateWizard(sessionId) {
    const srcId = sessionId || currentSession;
    if (!srcId) return;
    if (traitsCache.length === 0) await loadTraitsCache();
    renderWizardModal(`<h3>Duplicate Agent</h3><div class="loading">Loading...</div>`);
    let s;
    try {
      const resp = await apiCall('show', 'session', [srcId]);
      if (resp.status === 'error') {
        renderWizardModal(`<h3>Duplicate Agent</h3><div class="empty-state">Error: ${escapeHtml(resp.message)}</div>
          <div class="modal-actions"><button class="btn-muted" onclick="window._qewCloseWizard()">Close</button></div>`);
        return;
      }
      s = resp.data || {};
    } catch (e) {
      renderWizardModal(`<h3>Duplicate Agent</h3><div class="empty-state">Error: ${escapeHtml(e.message)}</div>
        <div class="modal-actions"><button class="btn-muted" onclick="window._qewCloseWizard()">Close</button></div>`);
      return;
    }
    wizardState = {
      step: 1, moneypennies: [], selectedMP: s.moneypenny || '',
      currentPath: s.path || '~', projects: projectsCache,
      copy: true, source: s, sourceId: srcId,
    };
    showWizardStep1();
  }

  function closeWizard() {
    cmdPaletteOpen = false;
    const overlay = document.querySelector('.modal-overlay');
    if (overlay) overlay.remove();
  }

  // --- In-conversation command palette ---

  // openCmdPalette shows a small keyboard-driven action menu for the open chat
  // session. Keys (handled by the global keydown listener) run the actions;
  // Escape closes it and returns focus to the chat input.
  function openCmdPalette() {
    if (!currentSession) return;
    // Close the Actions dropdown if it happens to be open.
    const menu = document.getElementById('chat-menu');
    if (menu) menu.classList.remove('open');
    renderWizardModal(`
      <div class="cmd-palette" tabindex="-1" role="dialog" aria-modal="true" aria-label="Session commands">
        <h3>Session Commands</h3>
        <div class="cmd-list">
          <button class="cmd-item" data-cmd="complete"><kbd>c</kbd> Complete session</button>
          <button class="cmd-item" data-cmd="edit"><kbd>e</kbd> Edit session</button>
          <button class="cmd-item" data-cmd="duplicate"><kbd>y</kbd> Duplicate session</button>
          <button class="cmd-item" data-cmd="diff"><kbd>g</kbd> Git diff</button>
          <button class="cmd-item" data-cmd="model"><kbd>o</kbd> Model override</button>
          <button class="cmd-item" data-cmd="effort"><kbd>f</kbd> Effort override</button>
          <button class="cmd-item" data-cmd="memory"><kbd>m</kbd> Memory</button>
          <button class="cmd-item" data-cmd="compact"><kbd>K</kbd> Compact session</button>
          <button class="cmd-item" data-cmd="distill"><kbd>D</kbd> Distill to memory</button>
          <button class="cmd-item" data-cmd="thoughts"><kbd>t</kbd> Toggle train of thought</button>
          <button class="cmd-item" data-cmd="stop"><kbd>s</kbd> Stop session</button>
          <button class="cmd-item" data-cmd="delete" style="color:var(--danger)"><kbd>d</kbd> Delete session</button>
          <button class="cmd-item" data-cmd="back"><kbd>q</kbd> Back to session list</button>
        </div>
        <div class="modal-actions"><button class="btn-muted" onclick="window._qewCloseCmdPalette()">Close (Esc)</button></div>
      </div>
    `);
    cmdPaletteOpen = true;
    const pal = document.querySelector('.cmd-palette');
    if (pal) {
      pal.addEventListener('click', (e) => {
        const btn = e.target.closest('.cmd-item');
        if (btn) runCmd(btn.dataset.cmd);
      });
      pal.focus();
    }
  }

  function closeCmdPalette(focusInput) {
    cmdPaletteOpen = false;
    closeWizard();
    if (focusInput) {
      const inp = document.getElementById('chat-input');
      if (inp) inp.focus();
    }
  }
  window._qewCloseCmdPalette = function() { closeCmdPalette(true); };

  // runCmd executes a command-palette action. It always tears down the palette
  // first (resetting cmdPaletteOpen) so the keydown handler won't keep routing
  // single keys to the palette while the follow-up modal/flow is showing.
  function runCmd(cmd) {
    cmdPaletteOpen = false;
    closeWizard();
    switch (cmd) {
      case 'complete': completeSession(); break;
      case 'edit':     showEditSessionModal(); break;
      case 'duplicate': openDuplicateWizard(); break;
      case 'diff':     showDiff(); break;
      case 'thoughts': toggleThoughts(); break;
      case 'model':    focusOverrideSelect('chat-model-override'); break;
      case 'effort':   focusOverrideSelect('chat-effort-override'); break;
      case 'memory':   openMemoryModal(); break;
      case 'compact':  compactSession(); break;
      case 'distill':  distillSession(); break;
      case 'stop':     stopSession(); break;
      case 'delete':   deleteSession(); break;
      case 'back':     closeChat(); break;
    }
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

    // Auto-select default — but in copy mode prefer the source's moneypenny
    // when it's still registered, so the duplicate lands on the same host.
    if (wizardState.copy && wizardState.selectedMP &&
        wizardState.moneypennies.find(m => m.name === wizardState.selectedMP)) {
      // keep source moneypenny
    } else {
      const def = wizardState.moneypennies.find(m => m.isDefault);
      if (def) wizardState.selectedMP = def.name;
      else wizardState.selectedMP = wizardState.moneypennies[0].name;
    }

    // If only one moneypenny, skip to step 2.
    if (wizardState.moneypennies.length === 1) {
      showWizardStep2();
      return;
    }

    renderWizardModal(`
      <h3>${wizardTitle()}</h3>
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
      <h3>${wizardTitle()}</h3>
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
        const errMsg = resp.message ? escapeHtml(resp.message) : 'Could not list directory';
        document.getElementById('wizard-dirs').innerHTML = `<div class="empty-state">${errMsg}</div>`;
      }
    } catch (e) {
      document.getElementById('wizard-dirs').innerHTML = `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  function showWizardStep3() {
    const copy = wizardState.copy;
    const src = copy ? (wizardState.source || {}) : {};
    const srcProject = copy ? (src.project || '') : '';
    let projectOpts = wizardState.projects.filter(p => p.status !== 'done')
      .map(p => `<option value="${escapeAttr(p.name)}"${p.name === srcProject ? ' selected' : ''}>${escapeHtml(p.name)}</option>`).join('');
    // Ensure the source's project is selectable even if it's done/archived.
    if (srcProject && !wizardState.projects.some(p => p.name === srcProject && p.status !== 'done')) {
      projectOpts += `<option value="${escapeAttr(srcProject)}" selected>${escapeHtml(srcProject)}</option>`;
    }

    const srcAgent = copy ? (src.agent || 'copilot') : 'copilot';
    const agents = ['copilot', 'claude'];
    if (!agents.includes(srcAgent)) agents.push(srcAgent);
    const agentOpts = agents.map(a => `<option value="${escapeAttr(a)}"${a === srcAgent ? ' selected' : ''}>${escapeHtml(a)}</option>`).join('');

    const defName = copy ? ('Copy of ' + (src.name || '')) : '';
    const promptLabel = copy ? 'Prompt (optional)' : 'Prompt *';
    const promptPlaceholder = copy ? 'Leave blank to acknowledge summary' : 'What should the agent do?';
    const submitLabel = copy ? 'Duplicate Agent' : 'Deploy Agent';
    // Seed the model dropdown with the source model so syncWizardAgentDeps
    // preserves it (it only keeps the prior selection if still valid for the
    // chosen agent).
    const modelSeed = copy && src.model
      ? `<option value="${escapeAttr(src.model)}" selected>${escapeHtml(src.model)}</option>`
      : '<option value="">(default)</option>';

    renderWizardModal(`
      <h3>${wizardTitle()}</h3>
      <div class="step-label">Step 3 of 3 — Session Details</div>
      <div style="font-size:0.85em;color:var(--muted);margin-bottom:8px">
        📡 ${escapeHtml(wizardState.selectedMP)} &nbsp; 📂 ${escapeHtml(wizardState.currentPath)}
      </div>
      <label for="wiz-prompt">${promptLabel}</label>
      <textarea id="wiz-prompt" rows="3" placeholder="${escapeAttr(promptPlaceholder)}"></textarea>
      <label for="wiz-name">Name (optional)</label>
      <input id="wiz-name" type="text" placeholder="Auto-generated from prompt" value="${escapeAttr(defName)}">
      <label for="wiz-project">Project</label>
      <select id="wiz-project"><option value="">(none)</option>${projectOpts}</select>
      <label for="wiz-agent">Agent</label>
      <select id="wiz-agent">${agentOpts}</select>
      <label for="wiz-model">Model</label>
      <select id="wiz-model">${modelSeed}</select>
      <label for="wiz-effort">Effort</label>
      <select id="wiz-effort">${effortOptionsHtml(srcAgent, copy ? (src.effort || '') : '')}</select>
      <label for="wiz-sysprompt">System Prompt (optional)</label>
      <textarea id="wiz-sysprompt" rows="2" placeholder="">${copy ? escapeHtml(src.system_prompt || '') : ''}</textarea>
      <div class="toggle-row">
        <input type="checkbox" id="wiz-yolo"${copy && src.yolo ? ' checked' : ''}>
        <label for="wiz-yolo" style="margin:0;color:var(--text)">License to Kill (skip permission prompts)</label>
      </div>
      <div class="toggle-row">
        <input type="checkbox" id="wiz-gadgets"${copy && src.gadgets ? ' checked' : ''}>
        <label for="wiz-gadgets" style="margin:0;color:var(--text)">Gadgets (include James tooling in system prompt)</label>
      </div>
      <label for="wiz-compaction">Compaction</label>
      <select id="wiz-compaction">
        <option value="custom"${(copy ? (src.compaction_mode || 'custom') : 'custom') === 'custom' ? ' selected' : ''}>Custom (distill to memory, then summarize)</option>
        <option value="agent"${(copy ? (src.compaction_mode || 'custom') : 'custom') === 'agent' ? ' selected' : ''}>Agent (rely on the agent's own compaction)</option>
      </select>
      ${traitsCache.length ? `<label>Traits</label><div id="wiz-traits" style="display:flex;flex-direction:column;gap:4px">` +
        traitsCache.map(t => { const checked = copy ? (Array.isArray(src.traits) && src.traits.includes(t.id)) : t.def; return `<div class="toggle-row"><input type="checkbox" class="wiz-trait" id="wiz-trait-${escapeAttr(t.id)}" value="${escapeAttr(t.id)}"${checked ? ' checked' : ''}><label for="wiz-trait-${escapeAttr(t.id)}" style="margin:0;color:var(--text)" title="${escapeAttr(t.preview)}">${escapeHtml(t.name)}</label></div>`; }).join('') +
        `</div>` : ''}
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewWizardBackToPath()">Back</button>
        <button class="btn" id="wiz-submit">${submitLabel}</button>
      </div>
    `);

    // Stash the prefilled system prompt so copy mode can detect user edits and
    // only override (skipping backend marker stripping) when actually changed.
    if (copy) wizardState.origSysPrompt = src.system_prompt || '';

    // If a project is pre-selected from the filter, select it — but in copy
    // mode the source's project takes precedence.
    if (projectFilter && !copy) {
      document.getElementById('wiz-project').value = projectFilter;
    }

    document.getElementById('wiz-submit').addEventListener('click', submitCreateSession);
    document.getElementById('wiz-agent').addEventListener('change', syncWizardAgentDeps);
    syncWizardAgentDeps();
  }

  // syncWizardAgentDeps repopulates the model and effort dropdowns to match the
  // currently selected agent (effort lists differ per agent; models are fetched
  // from the selected moneypenny).
  async function syncWizardAgentDeps() {
    const agentSel = document.getElementById('wiz-agent');
    if (!agentSel) return;
    const agent = agentSel.value;
    const effortSel = document.getElementById('wiz-effort');
    if (effortSel) effortSel.innerHTML = effortOptionsHtml(agent, effortSel.value);
    const modelSel = document.getElementById('wiz-model');
    if (modelSel) {
      const cur = modelSel.value;
      modelSel.innerHTML = '<option value="">(loading…)</option>';
      const models = await loadModels(wizardState.selectedMP, agent);
      // Guard against a stale fetch if the agent changed again meanwhile.
      if (agentSel.value !== agent) return;
      // Drop a previously-picked model that isn't valid for the new agent so
      // we never submit e.g. a copilot model with --agent claude. (Unlike the
      // edit dialog, there's no pre-existing session model worth preserving.)
      const keep = models.includes(cur) ? cur : '';
      modelSel.innerHTML = modelOptionsHtml(models, keep);
    }
  }

  async function submitCreateSession() {
    const copy = wizardState.copy;
    const submitLabel = copy ? 'Duplicate Agent' : 'Deploy Agent';
    const prompt = document.getElementById('wiz-prompt').value.trim();
    if (!copy && !prompt) {
      alert('Prompt is required');
      return;
    }

    // Copy mode leads with the source session ID (a positional arg the
    // `copy session` verb expects); create mode has no positional source.
    const args = copy ? [wizardState.sourceId] : [];
    args.push('-m', wizardState.selectedMP, '--path', wizardState.currentPath, '--async');
    const name = document.getElementById('wiz-name').value.trim();
    if (name) args.push('--name', name);
    const project = document.getElementById('wiz-project').value;
    if (project) args.push('--project', project);
    const agent = document.getElementById('wiz-agent').value.trim();
    if (agent) args.push('--agent', agent);
    const model = document.getElementById('wiz-model').value;
    if (model) args.push('--model', model);
    const effort = document.getElementById('wiz-effort').value;
    if (effort) args.push('--effort', effort);
    const sysprompt = document.getElementById('wiz-sysprompt').value.trim();
    if (copy) {
      // Only override the system prompt when the user actually edited it;
      // otherwise let the backend inherit + strip injected markers itself.
      if (sysprompt !== (wizardState.origSysPrompt || '').trim()) {
        args.push('--system-prompt', sysprompt);
      }
    } else if (sysprompt) {
      args.push('--system-prompt', sysprompt);
    }
    const yolo = document.getElementById('wiz-yolo').checked;
    if (copy) {
      // Emit an explicit boolean so unchecking genuinely disables a yolo
      // source (omitting --yolo would inherit the source's value).
      args.push('--yolo=' + (yolo ? 'true' : 'false'));
    } else if (yolo) {
      args.push('--yolo');
    }
    if (document.getElementById('wiz-gadgets').checked) args.push('--gadgets');
    const compaction = document.getElementById('wiz-compaction').value;
    if (compaction) args.push('--compaction', compaction);
    // Only emit explicit --traits once traits have loaded; emitting an empty
    // selection before traits are known would suppress backend default/source
    // traits. In copy mode also preserve any source traits not shown in the
    // checkbox list (e.g. a since-deleted trait definition).
    if (traitsCache.length) {
      let selTraits = Array.from(document.querySelectorAll('.wiz-trait:checked')).map(c => c.value);
      if (copy && Array.isArray(wizardState.source && wizardState.source.traits)) {
        const visible = new Set(traitsCache.map(t => t.id));
        const unknown = wizardState.source.traits.filter(id => !visible.has(id));
        selTraits = selTraits.concat(unknown);
      }
      args.push('--traits', selTraits.join(','));
    }
    // Prompt is the trailing positional; optional in copy mode.
    if (prompt || !copy) args.push(prompt);

    const btn = document.getElementById('wiz-submit');
    btn.disabled = true;
    btn.textContent = copy ? 'Duplicating...' : 'Creating...';

    try {
      const resp = await apiCall(copy ? 'copy' : 'create', 'session', args);
      if (resp.status === 'error') {
        alert('Error: ' + resp.message);
        btn.disabled = false;
        btn.textContent = submitLabel;
        return;
      }
      closeWizard();
      await loadDashboard();
    } catch (e) {
      alert('Error: ' + e.message);
      btn.disabled = false;
      btn.textContent = submitLabel;
    }
  }

  function renderWizardModal(content, modalClass) {
    let overlay = document.querySelector('.modal-overlay');
    if (!overlay) {
      overlay = document.createElement('div');
      overlay.className = 'modal-overlay';
      overlay.addEventListener('click', (e) => { if (e.target === overlay) closeWizard(); });
      document.body.appendChild(overlay);
    }
    overlay.innerHTML = `<div class="modal${modalClass ? ' ' + modalClass : ''}">${content}</div>`;
    // Auto-focus the first text input/textarea so keyboard users (and the
    // "new trait" / form modals) can start typing immediately. Skipped when an
    // element opts out via [autofocus] elsewhere or when there is none.
    const modalEl = overlay.querySelector('.modal');
    if (modalEl) {
      const first = modalEl.querySelector(
        'input:not([type=checkbox]):not([type=radio]):not([disabled]), textarea:not([disabled]), select:not([disabled])');
      if (first) {
        // Defer to ensure the element is laid out before focusing.
        requestAnimationFrame(() => { try { first.focus(); } catch (e) {} });
      }
    }
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

  // Git diff review state (clickable lines + inline comments). `mode` is
  // 'diff' (working-tree diff) or 'commit' (a specific commit's contents);
  // `commit` holds the hash in commit mode.
  let diffReview = { text: '', lines: [], comments: {}, branch: '', mode: 'diff', commit: '', cursor: 0 };
  // Monotonic token guarding against stale async responses (log -> commit ->
  // back -> another commit) overwriting the current view.
  let gitViewToken = 0;

  function parseHunkHeader(line) {
    const m = line.match(/@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if (!m) return [0, 0];
    return [parseInt(m[1], 10), parseInt(m[2], 10)];
  }

  // parseDiffLines mirrors hem's parseDiffMeta: walks the unified diff tracking
  // the current file and old/new line numbers so each line carries the metadata
  // needed to build a review comment (file, real line number, code). When
  // opts.commitPreamble is set (commit view, fed by `git show --stat --patch`),
  // lines before the first patch header (commit metadata + diffstat) are left
  // non-commentable since they carry no file/line context.
  function parseDiffLines(text, opts) {
    opts = opts || {};
    const lines = text.split('\n');
    const out = [];
    let currentFile = '';
    let oldLine = 0, newLine = 0;
    let inPatch = !opts.commitPreamble; // diff mode is always "in patch"
    for (let raw of lines) {
      raw = raw.replace(/\r+$/, '');
      const meta = { raw, file: currentFile, lineNum: 0, code: '', side: '', cls: '', commentable: false };
      if (raw.startsWith('+++ b/')) { currentFile = raw.slice(6); meta.file = currentFile; meta.cls = 'diff-header'; }
      else if (raw.startsWith('+++ ') || raw.startsWith('--- ')) { meta.cls = 'diff-header'; }
      else if (raw.startsWith('diff ')) {
        inPatch = true;
        const idx = raw.indexOf(' b/');
        if (idx >= 0) currentFile = raw.slice(idx + 3);
        meta.file = currentFile; meta.cls = 'diff-header';
      } else if (raw.startsWith('@@')) {
        const h = parseHunkHeader(raw); oldLine = h[0]; newLine = h[1];
        meta.cls = 'diff-hunk';
      } else if (raw.startsWith('+')) {
        meta.lineNum = newLine; meta.side = '+'; meta.code = raw.slice(1); meta.cls = 'diff-add';
        newLine++;
      } else if (raw.startsWith('-')) {
        meta.lineNum = oldLine; meta.side = '-'; meta.code = raw.slice(1); meta.cls = 'diff-del';
        oldLine++;
      } else if (raw.length > 0 && raw[0] === ' ') {
        meta.lineNum = newLine; meta.side = ' '; meta.code = raw.slice(1);
        oldLine++; newLine++;
      }
      meta.commentable = raw.trim() !== '' && inPatch;
      out.push(meta);
    }
    return out;
  }

  function renderDiffReview() {
    return diffReview.lines.map((m, seq) => {
      const escaped = escapeHtml(m.raw);
      const inner = m.cls ? `<span class="${m.cls}">${escaped}</span>` : escaped;
      const has = diffReview.comments[seq] != null;
      const cls = 'diff-line' + (m.commentable ? ' commentable' : '') + (has ? ' has-comment' : '');
      const attr = m.commentable ? ` data-seq="${seq}"` : '';
      return `<div class="${cls}"${attr}>${inner || ' '}</div><div class="diff-comment-slot" id="dcs-${seq}"></div>`;
    }).join('');
  }

  function renderDiffView() {
    const isCommit = diffReview.mode === 'commit';
    const title = isCommit
      ? `Commit <span style="color:var(--muted);font-size:0.85em">${escapeHtml((diffReview.commit || '').slice(0, 12))}</span>`
      : `Git Diff <span id="diff-branch-label" style="color:var(--muted);font-size:0.85em">${diffReview.branch ? '(' + escapeHtml(diffReview.branch) + ')' : ''}</span>`;
    const actions = isCommit
      ? `
        <button class="btn-muted" onclick="window._qewBackToLog()">Back</button>
        <button class="btn" id="diff-send-comments" style="display:none" onclick="window._qewDiffSendStep()">Send comments</button>`
      : `
        <button class="btn-muted" onclick="window._qewCloseWizard()">Close</button>
        <button class="btn" onclick="window._qewCommitFromDiff()">Commit</button>
        <button class="btn" onclick="window._qewCommitAndPush()">Commit &amp; Push</button>
        <button class="btn" id="diff-send-comments" style="display:none" onclick="window._qewDiffSendStep()">Send comments</button>`;
    renderWizardModal(`
      <h3>${title}</h3>
      <div class="diff-content" id="diff-review-content">${renderDiffReview()}</div>
      <div class="modal-actions">${actions}</div>
    `, 'modal-large');
    const content = document.getElementById('diff-review-content');
    content.addEventListener('click', (e) => {
      const lineEl = e.target.closest('.diff-line.commentable');
      if (!lineEl || !content.contains(lineEl)) return;
      openCommentEditor(parseInt(lineEl.dataset.seq, 10));
    });
    // The cursor follows the mouse: hovering a diff line moves the selection
    // there (without scrolling) so keyboard nav resumes from where you point.
    content.addEventListener('mousemove', (e) => {
      const lineEl = e.target.closest('.diff-line');
      if (!lineEl || !content.contains(lineEl)) return;
      const idx = Array.prototype.indexOf.call(content.querySelectorAll('.diff-line'), lineEl);
      if (idx >= 0 && idx !== diffReview.cursor) {
        diffReview.cursor = idx;
        applyDiffCursor(false);
      }
    });
    Object.keys(diffReview.comments).forEach(seq => renderSavedComment(parseInt(seq, 10)));
    updateSendBtn();
    // Start the keyboard cursor on the first commentable line.
    diffReview.cursor = diffReview.lines.findIndex(m => m.commentable);
    if (diffReview.cursor < 0) diffReview.cursor = 0;
    applyDiffCursor(false);
  }

  // applyDiffCursor highlights the line at diffReview.cursor and, when scroll is
  // true, keeps it within the scrollable diff pane. The Nth .diff-line element
  // corresponds to line sequence N (renderDiffReview emits them in order).
  function applyDiffCursor(scroll) {
    const content = document.getElementById('diff-review-content');
    if (!content) return;
    content.querySelectorAll('.diff-line.cursor').forEach(el => el.classList.remove('cursor'));
    const el = content.querySelectorAll('.diff-line')[diffReview.cursor];
    if (!el) return;
    el.classList.add('cursor');
    if (scroll) el.scrollIntoView({ block: 'nearest' });
  }

  // diffPageLines estimates how many lines fit in the visible diff pane, used
  // for half-page (ctrl+u/d) and full-page (PageUp/Down) cursor movement.
  function diffPageLines(content) {
    const ref = content.querySelector('.diff-line.cursor') || content.querySelector('.diff-line');
    const lh = ref && ref.offsetHeight ? ref.offsetHeight : 20;
    return Math.max(1, Math.floor(content.clientHeight / Math.max(1, lh)));
  }

  function moveDiffCursor(delta) {
    const n = diffReview.lines.length;
    if (!n) return;
    let c = diffReview.cursor;
    if (c == null || c < 0) c = 0;
    diffReview.cursor = Math.min(n - 1, Math.max(0, c + delta));
    applyDiffCursor(true);
  }

  function openCommentEditorAtCursor() {
    const m = diffReview.lines[diffReview.cursor];
    if (m && m.commentable) openCommentEditor(diffReview.cursor);
  }

  // handleDiffModalKey provides TUI-parity keyboard navigation for the git diff
  // review modal: j/k/arrows move one line, PageUp/Down a full page, ctrl+u/d a
  // half page, r opens the comment editor on the cursor line. Returns true if it
  // consumed the event. Typing in the inline comment editor is never hijacked.
  function handleDiffModalKey(e) {
    const content = document.getElementById('diff-review-content');
    if (!content || !diffReview.lines.length) return false;
    const tag = (e.target.tagName || '').toLowerCase();
    if (tag === 'textarea' || tag === 'input' || e.target.isContentEditable) return false;
    if (e.altKey) return false;
    if ((e.ctrlKey || e.metaKey) && (e.key === 'd' || e.key === 'D')) {
      e.preventDefault(); moveDiffCursor(Math.max(1, Math.floor(diffPageLines(content) / 2))); return true;
    }
    if ((e.ctrlKey || e.metaKey) && (e.key === 'u' || e.key === 'U')) {
      e.preventDefault(); moveDiffCursor(-Math.max(1, Math.floor(diffPageLines(content) / 2))); return true;
    }
    if (e.ctrlKey || e.metaKey) return false;
    switch (e.key) {
      case 'PageDown': e.preventDefault(); moveDiffCursor(diffPageLines(content)); return true;
      case 'PageUp':   e.preventDefault(); moveDiffCursor(-diffPageLines(content)); return true;
      case 'ArrowDown': case 'j': e.preventDefault(); moveDiffCursor(1); return true;
      case 'ArrowUp':   case 'k': e.preventDefault(); moveDiffCursor(-1); return true;
      case 'r': e.preventDefault(); openCommentEditorAtCursor(); return true;
    }
    return false;
  }

  function openCommentEditor(seq) {
    const slot = document.getElementById('dcs-' + seq);
    if (!slot) return;
    const existing = diffReview.comments[seq] || '';
    slot.innerHTML = `
      <div class="diff-comment-editor">
        <textarea id="dce-${seq}" rows="2" placeholder="Comment on this line...">${escapeHtml(existing)}</textarea>
        <div class="modal-actions">
          <button class="btn-muted" onclick="window._qewDiffCancelComment(${seq})">Cancel</button>
          ${existing ? `<button class="btn-muted" onclick="window._qewDiffRemoveComment(${seq})">Remove</button>` : ''}
          <button class="btn" onclick="window._qewDiffSaveComment(${seq})">Save</button>
        </div>
      </div>`;
    const ta = document.getElementById('dce-' + seq);
    ta.focus();
    ta.setSelectionRange(ta.value.length, ta.value.length);
    ta.addEventListener('keydown', (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') { e.preventDefault(); saveComment(seq); }
    });
  }

  function markLine(seq, on) {
    const el = document.querySelector(`.diff-line[data-seq="${seq}"]`);
    if (el) el.classList.toggle('has-comment', !!on);
  }

  function renderSavedComment(seq) {
    const slot = document.getElementById('dcs-' + seq);
    if (!slot) return;
    markLine(seq, true);
    const txt = diffReview.comments[seq] || '';
    slot.innerHTML = `
      <div class="diff-comment-saved">
        <div class="cmt-text">${escapeHtml(txt)}</div>
        <div class="cmt-actions">
          <a onclick="window._qewDiffEditComment(${seq})">Edit</a>
          <a onclick="window._qewDiffRemoveComment(${seq})">Remove</a>
        </div>
      </div>`;
  }

  function saveComment(seq) {
    const ta = document.getElementById('dce-' + seq);
    if (!ta) return;
    const val = ta.value.trim();
    if (!val) { removeComment(seq); return; }
    diffReview.comments[seq] = val;
    renderSavedComment(seq);
    updateSendBtn();
  }

  function removeComment(seq) {
    delete diffReview.comments[seq];
    markLine(seq, false);
    const slot = document.getElementById('dcs-' + seq);
    if (slot) slot.innerHTML = '';
    updateSendBtn();
  }

  function cancelComment(seq) {
    if (diffReview.comments[seq] != null) renderSavedComment(seq);
    else { const slot = document.getElementById('dcs-' + seq); if (slot) slot.innerHTML = ''; }
  }

  function updateSendBtn() {
    const btn = document.getElementById('diff-send-comments');
    if (!btn) return;
    const n = Object.keys(diffReview.comments).length;
    if (n > 0) { btn.style.display = ''; btn.textContent = `Send comments (${n})`; }
    else { btn.style.display = 'none'; }
  }

  function showSendCommentsStep() {
    const n = Object.keys(diffReview.comments).length;
    if (n === 0) return;
    renderWizardModal(`
      <h3>Send ${n} review comment${n > 1 ? 's' : ''}</h3>
      <label for="diff-overall">Overall comment (optional)</label>
      <textarea id="diff-overall" rows="4" placeholder="Add an overall instruction for the agent (optional)..."></textarea>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewDiffBackToReview()">Back</button>
        <button class="btn" id="diff-send-submit" onclick="window._qewDiffSubmitReview()">Send to agent</button>
      </div>
    `);
    const ta = document.getElementById('diff-overall');
    if (ta) ta.focus();
  }

  // formatReviewComment / buildReviewPrompt mirror hem/pkg/ui/diff.go so the
  // prompt sent to the agent is identical to the TUI's.
  function formatReviewComment(n, file, lineNum, code, comment) {
    let b = `\n## Comment ${n}\n\n`;
    if (file) {
      if (lineNum > 0) b += `Filename: \`${file}\`, line: ${lineNum}\n\n`;
      else b += `Filename: \`${file}\` (file header)\n\n`;
    } else if (lineNum > 0) {
      b += `Line: ${lineNum}\n\n`;
    }
    if (code && code.trim() !== '') {
      b += '```\n' + code + '\n```\n\n';
    }
    b += blockquote(comment) + '\n';
    return b;
  }

  // blockquote renders text as a Markdown blockquote (mirrors diff.go).
  function blockquote(s) {
    s = s.replace(/\n+$/, '');
    return s.split('\n').map(l => (l === '' ? '>' : '> ' + l)).join('\n');
  }

  function buildReviewPrompt(overall) {
    let b = '';
    if (overall && overall.trim() !== '') b += overall + '\n\n';
    if (diffReview.mode === 'commit') {
      b += "Here are some review comments on the changes in commit `" + (diffReview.commit || '') + "`. ";
      b += "If comments are questions, answer those questions. ";
      b += "If comments are unclear, or shouldn't be addressed, ask for feedback and confirmation. ";
      b += "Else address the comments.\n";
    } else {
      b += "Here are some review comments on the code currently in `git diff`. ";
      b += "If comments are questions, answer those questions. ";
      b += "If comments are unclear, or shouldn't be integrated, ask for feedback and confirmation. ";
      b += "Else integrate the comments.\n";
    }
    const grouped = Object.keys(diffReview.comments).map(seq => {
      const m = diffReview.lines[seq];
      return { file: m.file, lineNum: m.lineNum, code: m.code, comment: diffReview.comments[seq] };
    });
    grouped.sort((a, c) => a.file !== c.file ? (a.file < c.file ? -1 : 1) : a.lineNum - c.lineNum);
    grouped.forEach((fc, i) => { b += formatReviewComment(i + 1, fc.file, fc.lineNum, fc.code, fc.comment); });
    return b;
  }

  async function submitReview() {
    const n = Object.keys(diffReview.comments).length;
    if (n === 0 || !currentSession) return;
    const overallEl = document.getElementById('diff-overall');
    const overall = overallEl ? overallEl.value : '';
    const prompt = buildReviewPrompt(overall);
    const btn = document.getElementById('diff-send-submit');
    if (btn) { btn.disabled = true; btn.textContent = 'Sending...'; }
    try {
      await apiCall('continue', 'session', [currentSession, '--async', prompt]);
      closeWizard();
      queuedMessages.push({ content: prompt });
      lastChatHTML = '';
      await loadChat();
    } catch (e) {
      alert('Send error: ' + e.message);
      if (btn) { btn.disabled = false; btn.textContent = 'Send to agent'; }
    }
  }

  async function showDiff() {
    if (!currentSession) return;
    diffReview = { text: '', lines: [], comments: {}, branch: '', mode: 'diff', commit: '', cursor: 0 };
    const token = ++gitViewToken;
    const session = currentSession;
    renderWizardModal(`
      <h3>Git Diff <span id="diff-branch-label" style="color:var(--muted);font-size:0.85em"></span></h3>
      <div class="diff-content"><div class="loading">Loading diff...</div></div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Close</button>
      </div>
    `, 'modal-large');

    // Fetch branch name in parallel.
    apiCall('git-info', 'session', [currentSession]).then(resp => {
      if (resp.status === 'ok' && resp.data && resp.data.branch) {
        diffReview.branch = resp.data.branch;
        const el = document.getElementById('diff-branch-label');
        if (el) el.textContent = '(' + resp.data.branch + ')';
      }
    }).catch(() => {});

    try {
      const resp = await apiCall('diff', 'session', [currentSession]);
      if (token !== gitViewToken || currentSession !== session) return;
      const diffContainer = document.querySelector('.diff-content');
      if (resp.status === 'error') {
        if (diffContainer) diffContainer.innerHTML = `<span style="color:var(--danger)">${escapeHtml(resp.message)}</span>`;
        return;
      }
      const diffText = resp.data && resp.data.message ? resp.data.message : '';
      if (!diffText.trim()) {
        if (diffContainer) diffContainer.innerHTML = '<span style="color:var(--muted)">No changes (working tree clean)</span>';
        return;
      }
      diffReview.text = diffText;
      diffReview.lines = parseDiffLines(diffText);
      renderDiffView();
    } catch (e) {
      if (token !== gitViewToken || currentSession !== session) return;
      const c = document.querySelector('.diff-content');
      if (c) c.innerHTML = `<span style="color:var(--danger)">Error: ${escapeHtml(e.message)}</span>`;
    }
  }

  async function showGitLog() {
    if (!currentSession) return;
    const token = ++gitViewToken;
    const session = currentSession;
    renderWizardModal(`
      <h3>Git Log <span id="git-log-branch-label" style="color:var(--muted);font-size:0.85em"></span></h3>
      <div class="diff-content"><div class="loading">Loading git log...</div></div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Close</button>
      </div>
    `, 'modal-large');

    // Fetch branch name in parallel.
    apiCall('git-info', 'session', [currentSession]).then(resp => {
      if (token !== gitViewToken || currentSession !== session) return;
      if (resp.status === 'ok' && resp.data && resp.data.branch) {
        const el = document.getElementById('git-log-branch-label');
        if (el) el.textContent = '(' + resp.data.branch + ')';
      }
    }).catch(() => {});

    try {
      const resp = await apiCall('git-log', 'session', [currentSession]);
      if (token !== gitViewToken || currentSession !== session) return;
      const container = document.querySelector('.diff-content');
      if (resp.status === 'error') {
        container.innerHTML = `<span style="color:var(--danger)">${escapeHtml(resp.message)}</span>`;
        return;
      }
      const logText = resp.data && resp.data.message ? resp.data.message : '';
      if (!logText) {
        container.innerHTML = '<span style="color:var(--muted)">No commits</span>';
        return;
      }
      container.innerHTML = formatGitLog(logText);
      // Clicking a commit line opens that commit's contents for review.
      container.addEventListener('click', (e) => {
        const el = e.target.closest('.log-commit');
        if (el && el.dataset.hash) showCommit(el.dataset.hash);
      });
    } catch (e) {
      if (token !== gitViewToken || currentSession !== session) return;
      document.querySelector('.diff-content').innerHTML =
        `<span style="color:var(--danger)">Error: ${escapeHtml(e.message)}</span>`;
    }
  }

  // showCommit fetches a commit's contents (git show) and renders them in the
  // same review UI as the working-tree diff, so the user can leave inline
  // comments and send them to the agent.
  async function showCommit(hash) {
    if (!currentSession || !hash) return;
    diffReview = { text: '', lines: [], comments: {}, branch: diffReview.branch, mode: 'commit', commit: hash, cursor: 0 };
    const token = ++gitViewToken;
    const session = currentSession;
    renderWizardModal(`
      <h3>Commit <span style="color:var(--muted);font-size:0.85em">${escapeHtml(hash.slice(0, 12))}</span></h3>
      <div class="diff-content"><div class="loading">Loading commit...</div></div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewBackToLog()">Back</button>
      </div>
    `, 'modal-large');
    try {
      const resp = await apiCall('git-show', 'session', [currentSession, '--hash', hash]);
      if (token !== gitViewToken || currentSession !== session) return;
      const container = document.querySelector('.diff-content');
      if (resp.status === 'error') {
        if (container) container.innerHTML = `<span style="color:var(--danger)">${escapeHtml(resp.message)}</span>`;
        return;
      }
      const showText = resp.data && resp.data.message ? resp.data.message : '';
      if (!showText.trim()) {
        if (container) container.innerHTML = '<span style="color:var(--muted)">Empty commit</span>';
        return;
      }
      diffReview.text = showText;
      diffReview.lines = parseDiffLines(showText, { commitPreamble: true });
      renderDiffView();
    } catch (e) {
      if (token !== gitViewToken || currentSession !== session) return;
      const c = document.querySelector('.diff-content');
      if (c) c.innerHTML = `<span style="color:var(--danger)">Error: ${escapeHtml(e.message)}</span>`;
    }
  }

  function formatGitLog(text) {
    return text.split('\n').map(line => {
      const escaped = escapeHtml(line);
      // Color graph characters (*, |, /, \) and wrap commit lines so they're
      // clickable (the captured hash is hex, safe to embed as an attribute).
      return escaped.replace(/^([*|/\\ ]+)([0-9a-f]{7,})(.*)$/, (_, graph, hash, rest) => {
        let msg = rest;
        // Color decorations like (HEAD -> main)
        msg = msg.replace(/\(([^)]+)\)/g, '<span style="color:var(--success)">($1)</span>');
        return `<span class="log-commit" data-hash="${hash}"><span style="color:var(--warning)">${graph}</span><span style="color:var(--info)">${hash}</span>${msg}</span>`;
      });
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

  function focusOverrideSelect(id) {
    const sel = document.getElementById(id);
    if (sel) {
      sel.focus();
      // Open the native dropdown where supported (best-effort).
      if (typeof sel.showPicker === 'function') {
        try { sel.showPicker(); } catch (e) { /* ignore */ }
      }
    }
  }

  // openMemoryModal shows the session's hierarchical memory as a browsable tree
  // with a per-node editor and search (mirrors hem/pkg/ui/memory.go). Memory is
  // a tree of nodes (slash-delimited paths); each node has an optional
  // title/description and a body. Cmd/Ctrl+Enter saves in the editor; Esc backs
  // out (editor → tree → close).
  async function openMemoryModal() {
    if (!currentSession) return;
    const sid = currentSession;
    const state = { sid, nodes: [], view: 'browse', search: { term: '', results: [], done: false } };

    renderWizardModal(`<h3>Memory</h3><div class="loading">Loading...</div>`, 'modal-mem');
    await loadMemTree();
    if (currentSession !== sid) return;
    renderBrowse();

    async function loadMemTree() {
      try {
        const resp = await apiCall('show', 'memory', [sid]);
        if (resp.status === 'ok' && resp.data && Array.isArray(resp.data.nodes)) {
          state.nodes = resp.data.nodes;
        } else {
          state.nodes = [];
        }
      } catch (e) { state.nodes = []; }
    }

    function nodeRowsHtml(nodes, withIndent) {
      if (!nodes.length) return `<div class="mem-empty">(empty — create the first memory node)</div>`;
      return nodes.map(n => {
        const depth = withIndent ? (n.path.split('/').length - 1) : 0;
        const leaf = withIndent ? n.path.split('/').pop() : n.path;
        const desc = n.description || n.title || '';
        return `<div class="mem-node" data-path="${escapeHtml(n.path)}">` +
          `<span class="mem-path" style="padding-left:${depth * 16}px">${escapeHtml(leaf)}</span>` +
          `<span class="mem-desc">${escapeHtml(desc)}</span></div>`;
      }).join('');
    }

    function renderBrowse() {
      state.view = 'browse';
      const listHtml = state.search.done
        ? (state.search.error
            ? `<div class="mem-empty">Search error: ${escapeHtml(state.search.error)}</div>`
            : nodeRowsHtml(state.search.results, false))
        : nodeRowsHtml(state.nodes, true);
      renderWizardModal(`
        <h3>Memory</h3>
        <div class="mem-toolbar">
          <input id="mem-search" type="text" placeholder="Search memory…" value="${escapeHtml(state.search.term)}">
          <button class="btn-muted" id="mem-new">New node</button>
        </div>
        ${state.search.done ? `<div class="step-label">Results for "${escapeHtml(state.search.term)}" — Esc-clear search</div>` : ''}
        <div class="mem-tree" id="mem-tree">${listHtml}</div>
        <div class="modal-actions">
          <button class="btn-muted" onclick="window._qewCloseWizard()">Close</button>
        </div>
      `, 'modal-mem');

      const searchBox = document.getElementById('mem-search');
      if (searchBox) {
        searchBox.focus();
        searchBox.addEventListener('keydown', async (e) => {
          if (e.key === 'Enter') {
            e.preventDefault();
            const term = searchBox.value.trim();
            if (!term) { state.search = { term: '', results: [], done: false }; renderBrowse(); return; }
            try {
              const resp = await apiCall('search', 'memory', [sid, term]);
              if (resp.status === 'error') {
                state.search = { term, results: [], done: true, error: resp.message || 'search failed' };
              } else {
                const results = (resp.status === 'ok' && resp.data && Array.isArray(resp.data.results)) ? resp.data.results : [];
                state.search = { term, results, done: true };
              }
            } catch (err) { state.search = { term, results: [], done: true, error: err.message }; }
            renderBrowse();
          } else if (e.key === 'Escape' && state.search.done) {
            e.preventDefault();
            e.stopPropagation();
            state.search = { term: '', results: [], done: false };
            renderBrowse();
          }
        });
      }
      const newBtn = document.getElementById('mem-new');
      if (newBtn) newBtn.addEventListener('click', () => openEditor(null));
      document.querySelectorAll('#mem-tree .mem-node').forEach(el => {
        el.addEventListener('click', () => openEditor(el.getAttribute('data-path')));
      });
    }

    async function openEditor(path) {
      let node = { path: '', title: '', description: '', body: '' };
      const isNew = !path;
      if (path) {
        try {
          const resp = await apiCall('show', 'memory', [sid, path]);
          if (resp.status === 'ok' && resp.data && resp.data.node) node = resp.data.node;
          else { alert('Memory error: ' + (resp.message || 'node not found')); return; }
        } catch (e) { alert('Error: ' + e.message); return; }
      }
      if (currentSession !== sid) return;
      state.view = 'editor';
      renderWizardModal(`
        <h3>${isNew ? 'New Memory Node' : 'Edit Node'}</h3>
        <label for="mem-f-path">Path (slash-delimited, e.g. project/conventions)</label>
        <input id="mem-f-path" type="text" value="${escapeHtml(node.path)}" ${isNew ? '' : 'readonly'}>
        <label for="mem-f-body">Note (README.md — Markdown)</label>
        <textarea id="mem-f-body" placeholder="This becomes the folder's README.md. Use a heading and keep parents as a concise synthesis + index of child folders.">${escapeHtml(node.body || '')}</textarea>
        <div class="modal-actions">
          <button class="btn-muted" id="mem-back">Back</button>
          ${isNew ? '' : '<button class="btn-muted" id="mem-delete">Delete</button>'}
          <button class="btn" id="mem-save">Save</button>
        </div>
      `, 'modal-mem');

      const pathEl = document.getElementById('mem-f-path');
      const bodyEl = document.getElementById('mem-f-body');
      if (isNew && pathEl) pathEl.focus(); else if (bodyEl) bodyEl.focus();

      document.getElementById('mem-back').addEventListener('click', () => renderBrowse());

      const delBtn = document.getElementById('mem-delete');
      if (delBtn) {
        delBtn.addEventListener('click', async () => {
          const p = node.path;
          if (!confirm(`Delete "${p}" and all descendants?`)) return;
          try {
            const resp = await apiCall('delete', 'memory', [sid, p, '--recursive']);
            if (resp.status === 'error') { alert('Memory error: ' + resp.message); return; }
          } catch (e) { alert('Error: ' + e.message); return; }
          await loadMemTree();
          state.search = { term: '', results: [], done: false };
          renderBrowse();
        });
      }

      const saveBtn = document.getElementById('mem-save');
      saveBtn.addEventListener('click', async () => {
        const p = document.getElementById('mem-f-path').value.trim();
        const body = document.getElementById('mem-f-body').value;
        if (!p) { alert('Path is required.'); return; }
        saveBtn.disabled = true;
        saveBtn.textContent = 'Saving...';
        // sid + path first, then body positional (the README content).
        const args = [sid, p, body];
        try {
          const resp = await apiCall('update', 'memory', args);
          if (resp.status === 'error') {
            alert('Memory error: ' + resp.message);
            saveBtn.disabled = false;
            saveBtn.textContent = 'Save';
            return;
          }
        } catch (e) {
          alert('Error: ' + e.message);
          saveBtn.disabled = false;
          saveBtn.textContent = 'Save';
          return;
        }
        await loadMemTree();
        state.search = { term: '', results: [], done: false };
        renderBrowse();
      });
    }
  }

  // --- Passkeys (WebAuthn) ---

  function _b64urlToBuf(s) {
    s = s.replace(/-/g, '+').replace(/_/g, '/');
    while (s.length % 4) s += '=';
    const bin = atob(s);
    const b = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) b[i] = bin.charCodeAt(i);
    return b.buffer;
  }
  function _bufToB64url(buf) {
    const b = new Uint8Array(buf);
    let s = '';
    for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }
  const _waHeaders = { 'Content-Type': 'application/json', 'X-Requested-With': 'QewClient' };

  async function registerPasskey() {
    if (!window.PublicKeyCredential) { alert('This browser does not support passkeys.'); return; }
    const label = window.prompt('Name this passkey (e.g. "MacBook", "iPhone"):', 'passkey');
    if (label === null) return;
    try {
      const r = await fetch('/webauthn/register/begin', { method: 'POST', headers: _waHeaders });
      if (!r.ok) throw new Error('Could not start registration');
      const opts = (await r.json()).publicKey;
      opts.challenge = _b64urlToBuf(opts.challenge);
      opts.user.id = _b64urlToBuf(opts.user.id);
      if (opts.excludeCredentials) opts.excludeCredentials.forEach(c => c.id = _b64urlToBuf(c.id));
      const cred = await navigator.credentials.create({ publicKey: opts });
      const body = {
        id: cred.id, rawId: _bufToB64url(cred.rawId), type: cred.type,
        response: {
          attestationObject: _bufToB64url(cred.response.attestationObject),
          clientDataJSON: _bufToB64url(cred.response.clientDataJSON),
        },
      };
      const f = await fetch('/webauthn/register/finish?label=' + encodeURIComponent(label),
        { method: 'POST', headers: _waHeaders, body: JSON.stringify(body) });
      if (!f.ok) throw new Error('Registration failed');
      openPasskeyModal();
    } catch (e) {
      alert('Passkey error: ' + (e.message || e));
    }
  }

  async function deletePasskey(id) {
    if (!confirm('Remove this passkey?')) return;
    try {
      const r = await fetch('/webauthn/credentials/delete',
        { method: 'POST', headers: _waHeaders, body: JSON.stringify({ id }) });
      if (!r.ok) throw new Error('Could not delete');
      openPasskeyModal();
    } catch (e) {
      alert('Error: ' + (e.message || e));
    }
  }
  window._qewDeletePasskey = deletePasskey;

  async function openPasskeyModal() {
    renderWizardModal(`<h3>Passkeys</h3><div class="loading">Loading...</div>`);
    let list = [];
    try {
      const r = await fetch('/webauthn/credentials', { headers: _waHeaders });
      if (r.ok) list = await r.json();
      else throw new Error('Passkeys are not available (no password configured).');
    } catch (e) {
      renderWizardModal(`
        <h3>Passkeys</h3>
        <div class="empty-state">${escapeHtml(e.message || 'Unavailable')}</div>
        <div class="modal-actions"><button class="btn-muted" onclick="window._qewCloseWizard()">Close</button></div>
      `);
      return;
    }
    const rows = (list && list.length)
      ? list.map(c => `
          <div class="passkey-row" style="display:flex;align-items:center;justify-content:space-between;padding:8px 0;border-bottom:1px solid var(--surface2)">
            <span>${escapeHtml(c.label || 'passkey')}<br><span style="color:var(--muted);font-size:0.85em">${new Date(c.createdAt).toLocaleString()}</span></span>
            <button class="btn-muted" onclick="window._qewDeletePasskey('${escapeHtml(c.id)}')">Remove</button>
          </div>`).join('')
      : `<div class="empty-state">No passkeys registered yet.</div>`;
    renderWizardModal(`
      <h3>Passkeys</h3>
      <p style="color:var(--muted);margin-bottom:12px">Register this device so you can sign in without the password.</p>
      ${rows}
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Close</button>
        <button class="btn" id="passkey-register-btn">+ Register a passkey</button>
      </div>
    `);
    const rb = document.getElementById('passkey-register-btn');
    if (rb) rb.addEventListener('click', registerPasskey);
  }

  async function createNewSubagent() {
    if (!currentSession) return;
    const prompt = window.prompt('Subagent prompt:');
    if (!prompt) return;
    try {
      const resp = await apiCall('create', 'subsession', [currentSession, '--async', '--yolo', prompt]);
      if (resp.status === 'error') {
        alert('Error: ' + resp.message);
        return;
      }
      // Open the new subagent chat.
      const sid = resp.data && resp.data.session_id;
      const name = resp.data && resp.data.name;
      if (sid) {
        window._openSubagent(sid, name || sid.substring(0, 12));
      } else {
        lastChatHTML = '';
        await loadChat();
      }
    } catch (e) {
      alert('Error: ' + e.message);
    }
  }

  async function stopSession() {
    if (!currentSession) return;
    try {
      const resp = await apiCall('stop', 'session', [currentSession]);
      if (resp.status === 'error') {
        alert('Stop error: ' + resp.message);
        return;
      }
      lastChatHTML = '';
      await loadChat();
    } catch (e) {
      alert('Error: ' + e.message);
    }
  }

  async function compactSession() {
    if (!currentSession) return;
    try {
      const resp = await apiCall('compact', 'session', [currentSession]);
      if (resp.status === 'error') {
        alert('Compaction error: ' + resp.message);
        return;
      }
      lastChatHTML = '';
      await loadChat();
    } catch (e) {
      alert('Error: ' + e.message);
    }
  }

  async function distillSession() {
    if (!currentSession) return;
    try {
      const resp = await apiCall('distillate', 'session', [currentSession]);
      if (resp.status === 'error') {
        alert('Distillation error: ' + resp.message);
        return;
      }
      lastChatHTML = '';
      await loadChat();
    } catch (e) {
      alert('Error: ' + e.message);
    }
  }

  async function completeSession(sessionId) {
    const sid = sessionId || currentSession;
    if (!sid) return;
    try {
      const resp = await apiCall('complete', 'session', [sid]);
      if (resp.status === 'error') {
        alert('Complete error: ' + resp.message);
        return;
      }
      if (sid === currentSession) closeChat();
      else loadDashboard();
    } catch (e) {
      alert('Error: ' + e.message);
    }
  }

  window._qewCommitFromDiff = function() { showCommitModal(false); };
  window._qewCommitAndPush = function() { showCommitModal(true); };
  window._qewDiffSendStep = showSendCommentsStep;
  window._qewDiffBackToReview = renderDiffView;
  window._qewDiffSubmitReview = submitReview;
  window._qewDiffSaveComment = saveComment;
  window._qewDiffRemoveComment = removeComment;
  window._qewDiffCancelComment = cancelComment;
  window._qewDiffEditComment = openCommentEditor;
  window._qewBackToLog = function() {
    // Warn before discarding unsent commit comments (Back navigates away).
    if (Object.keys(diffReview.comments).length > 0 &&
        !confirm('Discard unsent comments and return to the log?')) {
      return;
    }
    showGitLog();
  };

  // --- Session Edit & Delete ---

  async function showEditSessionModal(sessionId) {
    const sid = sessionId || currentSession;
    if (!sid) return;
    // Render the loading modal immediately (before any await) so opening from
    // a closed command palette never leaves a gap with no modal showing.
    renderWizardModal(`
      <h3>Edit Session</h3>
      <div class="loading">Loading...</div>
    `);
    if (traitsCache.length === 0) await loadTraitsCache();
    try {
      const resp = await apiCall('show', 'session', [sid]);
      if (resp.status === 'error') {
        renderWizardModal(`<h3>Edit Session</h3><div class="empty-state">Error: ${escapeHtml(resp.message)}</div>
          <div class="modal-actions"><button class="btn-muted" onclick="window._qewCloseWizard()">Close</button></div>`);
        return;
      }
      const s = resp.data || {};
      const selectedTraits = Array.isArray(s.traits) ? s.traits : [];
      const projectOpts = projectsCache.filter(p => p.status !== 'done')
        .map(p => `<option value="${escapeAttr(p.name)}"${p.name === (s.project || '') ? ' selected' : ''}>${escapeHtml(p.name)}</option>`).join('');

      renderWizardModal(`
        <h3>Edit Session</h3>
        <div style="font-size:0.85em;color:var(--muted);margin-bottom:8px">
          🕴️ ${escapeHtml(s.agent || 'copilot')} &nbsp; 📡 ${escapeHtml(s.moneypenny || '')} &nbsp; 📂 ${escapeHtml(s.path || '')}
        </div>
        <label for="es-name">Name</label>
        <input id="es-name" type="text" value="${escapeAttr(s.name || '')}">
        <label for="es-project">Project</label>
        <select id="es-project"><option value="">(none)</option>${projectOpts}</select>
        <label for="es-model">Model</label>
        <select id="es-model"><option value="">(loading…)</option></select>
        <label for="es-effort">Effort</label>
        <select id="es-effort">${effortOptionsHtml(s.agent || 'copilot', s.effort || '')}</select>
        <label for="es-sysprompt">System Prompt</label>
        <textarea id="es-sysprompt" rows="6">${escapeHtml(s.system_prompt || '')}</textarea>
        <div class="toggle-row">
          <input type="checkbox" id="es-yolo" ${s.yolo ? 'checked' : ''}>
          <label for="es-yolo" style="margin:0;color:var(--text)">License to Kill</label>
        </div>
        <div class="toggle-row">
          <input type="checkbox" id="es-gadgets" ${s.gadgets ? 'checked' : ''}>
          <label for="es-gadgets" style="margin:0;color:var(--text)">Gadgets (include James tooling in system prompt)</label>
        </div>
        <label for="es-compaction">Compaction</label>
        <select id="es-compaction">
          <option value="agent"${(s.compaction_mode || 'agent') === 'agent' ? ' selected' : ''}>Agent (rely on the agent's own compaction)</option>
          <option value="custom"${(s.compaction_mode || 'agent') === 'custom' ? ' selected' : ''}>Custom (distill to memory, then summarize)</option>
        </select>
        ${traitsCache.length ? `<label>Traits</label><div id="es-traits" style="display:flex;flex-direction:column;gap:4px">` +
          traitsCache.map(t => `<div class="toggle-row"><input type="checkbox" class="es-trait" id="es-trait-${escapeAttr(t.id)}" value="${escapeAttr(t.id)}"${selectedTraits.includes(t.id) ? ' checked' : ''}><label for="es-trait-${escapeAttr(t.id)}" style="margin:0;color:var(--text)" title="${escapeAttr(t.preview)}">${escapeHtml(t.name)}</label></div>`).join('') +
          `</div>` : ''}
        <div class="modal-actions">
          <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
          <button class="btn" id="es-submit">Save</button>
        </div>
      `, 'modal-edit');

      // Populate the model dropdown asynchronously from the session's
      // moneypenny + agent, preserving the session's current model.
      (async () => {
        const models = await loadModels(s.moneypenny, s.agent);
        const sel = document.getElementById('es-model');
        if (sel) sel.innerHTML = modelOptionsHtml(models, s.model || '');
      })();

      document.getElementById('es-submit').addEventListener('click', async () => {
        const args = [sid];
        const name = document.getElementById('es-name').value.trim();
        if (name && name !== (s.name || '')) args.push('--name', name);
        const project = document.getElementById('es-project').value;
        if (project !== (s.project || '')) args.push('--project', project);
        const model = document.getElementById('es-model').value;
        if (model && model !== (s.model || '')) args.push('--model', model);
        const effort = document.getElementById('es-effort').value;
        // Empty selection clears any override; the backend uses "none" as the
        // clear sentinel (an empty value is treated as "unchanged").
        if (effort !== (s.effort || '')) args.push('--effort', effort === '' ? 'none' : effort);
        const sysprompt = document.getElementById('es-sysprompt').value.trim();
        const yolo = document.getElementById('es-yolo').checked;
        if (yolo !== !!s.yolo) args.push('--yolo', yolo ? 'true' : 'false');
        // Gadgets toggle: when it changes, let the backend recompose the system
        // prompt (append/strip the gadgets block). Sending --system-prompt in the
        // same call would clobber that recomposition, so they're mutually exclusive
        // (mirrors the TUI edit form).
        const gadgets = document.getElementById('es-gadgets').checked;
        const gadgetsChanged = gadgets !== !!s.gadgets;
        if (gadgetsChanged) {
          args.push('--gadgets', gadgets ? 'true' : 'false');
        } else if (sysprompt !== (s.system_prompt || '')) {
          args.push('--system-prompt', sysprompt);
        }
        // Traits: emit --traits (possibly empty) only when the selection changed.
        const newTraits = Array.from(document.querySelectorAll('.es-trait:checked')).map(c => c.value);
        const origSorted = [...selectedTraits].sort().join(',');
        const newSorted = [...newTraits].sort().join(',');
        if (origSorted !== newSorted) args.push('--traits', newTraits.join(','));
        const compaction = document.getElementById('es-compaction').value;
        if (compaction !== (s.compaction_mode || 'agent')) args.push('--compaction', compaction);

        if (args.length <= 1) { closeWizard(); return; }

        const btn = document.getElementById('es-submit');
        btn.disabled = true;
        btn.textContent = 'Saving...';
        try {
          const resp = await apiCall('update', 'session', args);
          if (resp.status === 'error') {
            alert('Error: ' + resp.message);
            btn.disabled = false;
            btn.textContent = 'Save';
            return;
          }
          closeWizard();
          // Update displayed name if changed (only when editing the open chat).
          if (sid === currentSession && name && name !== currentSessionName) {
            currentSessionName = name;
            document.getElementById('chat-title').textContent = name;
          } else if (sid !== currentSession) {
            loadDashboard();
          }
        } catch (e) {
          alert('Error: ' + e.message);
          btn.disabled = false;
          btn.textContent = 'Save';
        }
      });
    } catch (e) {
      renderWizardModal(`<h3>Edit Session</h3><div class="empty-state">Error: ${escapeHtml(e.message)}</div>
        <div class="modal-actions"><button class="btn-muted" onclick="window._qewCloseWizard()">Close</button></div>`);
    }
  }

  async function deleteSession(sessionId, name) {
    const sid = sessionId || currentSession;
    const sname = name || currentSessionName;
    if (!sid) return;
    if (!confirm('Delete session "' + sname + '"? This cannot be undone.')) return;
    try {
      const resp = await apiCall('delete', 'session', [sid]);
      if (resp.status === 'error') {
        alert('Delete error: ' + resp.message);
        return;
      }
      if (sid === currentSession) closeChat();
      else loadDashboard();
    } catch (e) {
      alert('Error: ' + e.message);
    }
  }

  function showMoveToProjectModal() {
    if (!currentSession) return;
    const projectOpts = projectsCache.filter(p => p.status !== 'done')
      .map(p => `<option value="${escapeAttr(p.name)}">${escapeHtml(p.name)}</option>`).join('');

    renderWizardModal(`
      <h3>Move to Project</h3>
      <label for="move-project">Project</label>
      <select id="move-project"><option value="">(none — remove from project)</option>${projectOpts}</select>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" id="move-project-submit">Move</button>
      </div>
    `);

    document.getElementById('move-project-submit').addEventListener('click', async () => {
      const project = document.getElementById('move-project').value;
      const btn = document.getElementById('move-project-submit');
      btn.disabled = true;
      btn.textContent = 'Moving...';
      try {
        const resp = await apiCall('update', 'session', [currentSession, '--project', project]);
        if (resp.status === 'error') {
          alert('Error: ' + resp.message);
          btn.disabled = false;
          btn.textContent = 'Move';
          return;
        }
        closeWizard();
      } catch (e) {
        alert('Error: ' + e.message);
        btn.disabled = false;
        btn.textContent = 'Move';
      }
    });
  }

  // --- Moneypennies Management ---

  function showMoneypenniesView() {
    document.getElementById('dashboard-view').style.display = 'none';
    document.getElementById('moneypennies-view').style.display = 'flex';
    stopDashboardPoll();
    mgmtCursor = 0;
    loadMoneypennies();
  }

  function hideMoneypenniesView() {
    document.getElementById('moneypennies-view').style.display = 'none';
    document.getElementById('dashboard-view').style.display = 'flex';
    loadDashboard();
    startDashboardPoll();
  }

  async function loadMoneypennies() {
    const container = document.getElementById('mp-content');
    container.innerHTML = '<div class="loading">Loading...</div>';
    try {
      const resp = await apiCall('list', 'moneypenny', []);
      if (resp.status === 'error') {
        container.innerHTML = `<div class="empty-state">Error: ${escapeHtml(resp.message)}</div>`;
        return;
      }
      const rows = (resp.data && resp.data.rows) || [];
      if (rows.length === 0) {
        container.innerHTML = '<div class="empty-state">No moneypennies registered</div>';
        return;
      }
      let html = '';
      for (const r of rows) {
        const name = r[0], type = r[1], addr = r[2], isDef = r[3] === '*';
        const enabled = r.length > 4 ? r[4] !== 'false' : true;
        html += `
          <div class="mgmt-row${!enabled ? ' disabled' : ''}">
            <span class="mgmt-name">${escapeHtml(name)}</span>
            <span class="mgmt-detail">${escapeHtml(type)}${addr ? ' — ' + escapeHtml(addr) : ''}</span>
            ${isDef ? '<span class="mgmt-badge default">default</span>' : ''}
            ${!enabled ? '<span class="mgmt-badge danger">disabled</span>' : ''}
            <span class="mgmt-actions">
              <button data-mp="${escapeAttr(name)}" data-action="ping">Ping</button>
              <button data-mp="${escapeAttr(name)}" data-action="toggle-enabled">${enabled ? 'Disable' : 'Enable'}</button>
              ${!isDef ? `<button data-mp="${escapeAttr(name)}" data-action="set-default">Set Default</button>` : ''}
              <button data-mp="${escapeAttr(name)}" data-action="delete" class="danger">Delete</button>
            </span>
          </div>`;
      }
      container.innerHTML = html;
      container.querySelectorAll('.mgmt-actions button').forEach(btn => {
        btn.addEventListener('click', (e) => {
          e.stopPropagation();
          const mp = btn.dataset.mp;
          const action = btn.dataset.action;
          if (action === 'ping') pingMoneypenny(mp);
          else if (action === 'toggle-enabled') toggleMoneypennyEnabled(mp);
          else if (action === 'set-default') setDefaultMoneypenny(mp);
          else if (action === 'delete') deleteMoneypenny(mp);
        });
      });
      applyMgmtCursor(container, false);
    } catch (e) {
      container.innerHTML = `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  async function pingMoneypenny(name) {
    try {
      const resp = await apiCall('ping', 'moneypenny', ['-n', name]);
      if (resp.status === 'error') alert('Ping failed: ' + resp.message);
      else alert('Ping OK: ' + (resp.data && resp.data.message ? resp.data.message : 'success'));
    } catch (e) { alert('Error: ' + e.message); }
  }

  async function toggleMoneypennyEnabled(name) {
    try {
      // Check current state by looking at the button text.
      const btn = document.querySelector(`button[data-mp="${name}"][data-action="toggle-enabled"]`);
      const verb = btn && btn.textContent.trim() === 'Disable' ? 'disable' : 'enable';
      const resp = await apiCall(verb, 'moneypenny', ['-n', name]);
      if (resp.status === 'error') alert('Error: ' + resp.message);
      else loadMoneypennies();
    } catch (e) { alert('Error: ' + e.message); }
  }

  async function setDefaultMoneypenny(name) {
    try {
      const resp = await apiCall('set-default', 'moneypenny', ['-n', name]);
      if (resp.status === 'error') alert('Error: ' + resp.message);
      else loadMoneypennies();
    } catch (e) { alert('Error: ' + e.message); }
  }

  async function deleteMoneypenny(name) {
    if (!confirm('Delete moneypenny "' + name + '"? This will also remove all tracked sessions for it.')) return;
    try {
      const resp = await apiCall('delete', 'moneypenny', ['-n', name]);
      if (resp.status === 'error') alert('Error: ' + resp.message);
      else loadMoneypennies();
    } catch (e) { alert('Error: ' + e.message); }
  }

  function showAddMoneypennyModal() {
    renderWizardModal(`
      <h3>Add Moneypenny</h3>
      <label for="mp-name">Name *</label>
      <input id="mp-name" type="text" placeholder="my-moneypenny">
      <label for="mp-type">Type</label>
      <select id="mp-type">
        <option value="local">Local (default FIFO)</option>
        <option value="fifo">FIFO (custom path)</option>
        <option value="mi6">MI6 (remote)</option>
      </select>
      <div id="mp-fifo-fields" style="display:none">
        <label for="mp-fifo-folder">FIFO Folder</label>
        <input id="mp-fifo-folder" type="text" placeholder="~/.config/james/moneypenny/fifo">
      </div>
      <div id="mp-mi6-fields" style="display:none">
        <label for="mp-mi6-addr">MI6 Address</label>
        <input id="mp-mi6-addr" type="text" placeholder="host:port/session_id">
      </div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" id="mp-submit">Add</button>
      </div>
    `);

    const typeSelect = document.getElementById('mp-type');
    typeSelect.addEventListener('change', () => {
      document.getElementById('mp-fifo-fields').style.display = typeSelect.value === 'fifo' ? 'block' : 'none';
      document.getElementById('mp-mi6-fields').style.display = typeSelect.value === 'mi6' ? 'block' : 'none';
    });

    document.getElementById('mp-submit').addEventListener('click', async () => {
      const name = document.getElementById('mp-name').value.trim();
      if (!name) { alert('Name is required'); return; }
      const type = typeSelect.value;
      const args = ['-n', name];
      if (type === 'local') args.push('--local');
      else if (type === 'fifo') {
        const folder = document.getElementById('mp-fifo-folder').value.trim();
        if (!folder) { alert('FIFO folder is required'); return; }
        args.push('--fifo-folder', folder);
      } else if (type === 'mi6') {
        const addr = document.getElementById('mp-mi6-addr').value.trim();
        if (!addr) { alert('MI6 address is required'); return; }
        args.push('--mi6', addr);
      }

      const btn = document.getElementById('mp-submit');
      btn.disabled = true;
      btn.textContent = 'Adding...';
      try {
        const resp = await apiCall('add', 'moneypenny', args);
        if (resp.status === 'error') {
          alert('Error: ' + resp.message);
          btn.disabled = false;
          btn.textContent = 'Add';
          return;
        }
        closeWizard();
        loadMoneypennies();
      } catch (e) {
        alert('Error: ' + e.message);
        btn.disabled = false;
        btn.textContent = 'Add';
      }
    });
  }

  // --- Projects Management ---

  function showProjectsView() {
    document.getElementById('dashboard-view').style.display = 'none';
    document.getElementById('projects-view').style.display = 'flex';
    stopDashboardPoll();
    loadProjectsList();
  }

  function hideProjectsView() {
    document.getElementById('projects-view').style.display = 'none';
    document.getElementById('dashboard-view').style.display = 'flex';
    loadProjects(); // refresh cache
    loadDashboard();
    startDashboardPoll();
  }

  async function loadProjectsList() {
    const container = document.getElementById('proj-content');
    container.innerHTML = '<div class="loading">Loading...</div>';
    try {
      const resp = await apiCall('list', 'project', []);
      if (resp.status === 'error') {
        container.innerHTML = `<div class="empty-state">Error: ${escapeHtml(resp.message)}</div>`;
        return;
      }
      const rows = (resp.data && resp.data.rows) || [];
      if (rows.length === 0) {
        container.innerHTML = '<div class="empty-state">No projects</div>';
        return;
      }
      let html = '';
      for (const r of rows) {
        const id = r[0], name = r[1], status = r[2], mp = r[3], agent = r[4], paths = r[5];
        html += `
          <div class="mgmt-row" data-project-name="${escapeAttr(name)}" style="cursor:pointer">
            <span class="mgmt-name">${escapeHtml(name)}</span>
            <span class="mgmt-badge ${status}">${escapeHtml(status)}</span>
            ${mp ? `<span class="mgmt-detail">${escapeHtml(mp)}</span>` : ''}
            ${agent ? `<span class="mgmt-detail">${escapeHtml(agent)}</span>` : ''}
            <span class="mgmt-actions">
              <button data-proj-name="${escapeAttr(name)}" data-action="edit">Edit</button>
              <button data-proj-name="${escapeAttr(name)}" data-action="delete" class="danger">Delete</button>
            </span>
          </div>`;
      }
      container.innerHTML = html;
      // Click row to filter dashboard by project.
      container.querySelectorAll('.mgmt-row').forEach(row => {
        row.addEventListener('click', (e) => {
          if (e.target.closest('.mgmt-actions')) return;
          const projName = row.dataset.projectName;
          projectFilter = projName;
          document.getElementById('project-filter').value = projName;
          hideProjectsView();
        });
      });
      container.querySelectorAll('.mgmt-actions button').forEach(btn => {
        btn.addEventListener('click', (e) => {
          e.stopPropagation();
          const projName = btn.dataset.projName;
          const action = btn.dataset.action;
          if (action === 'edit') showEditProjectModal(projName);
          else if (action === 'delete') deleteProject(projName);
        });
      });
    } catch (e) {
      container.innerHTML = `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  async function deleteProject(name) {
    if (!confirm('Delete project "' + name + '"?')) return;
    try {
      const resp = await apiCall('delete', 'project', [name]);
      if (resp.status === 'error') alert('Error: ' + resp.message);
      else loadProjectsList();
    } catch (e) { alert('Error: ' + e.message); }
  }

  let projFormState = { selectedMP: '', currentPath: '', moneypennies: [] };

  async function showCreateProjectModal() {
    projFormState = { selectedMP: '', currentPath: '', moneypennies: [] };

    // Load moneypennies for dropdown.
    try {
      const resp = await apiCall('list', 'moneypenny', []);
      if (resp.status === 'ok' && resp.data && resp.data.rows) {
        projFormState.moneypennies = resp.data.rows.map(r => ({
          name: r[0], type: r[1], isDefault: r[3] === '*',
        }));
        const def = projFormState.moneypennies.find(m => m.isDefault);
        if (def) projFormState.selectedMP = def.name;
        else if (projFormState.moneypennies.length > 0) projFormState.selectedMP = projFormState.moneypennies[0].name;
      }
    } catch (e) { /* ignore */ }

    const mpOpts = projFormState.moneypennies
      .map(m => `<option value="${escapeAttr(m.name)}"${m.name === projFormState.selectedMP ? ' selected' : ''}>${escapeHtml(m.name)} (${escapeHtml(m.type)})</option>`).join('');

    renderWizardModal(`
      <h3>New Project</h3>
      <label for="proj-name">Name *</label>
      <input id="proj-name" type="text" placeholder="my-project">
      <label for="proj-mp">Default Moneypenny</label>
      <select id="proj-mp"><option value="">(none)</option>${mpOpts}</select>
      <label>Path</label>
      <div style="display:flex;align-items:center;gap:8px;margin-bottom:4px">
        <input id="proj-path" type="text" placeholder="(none)" style="flex:1" readonly>
        <button class="btn-muted" id="proj-browse-btn">Browse</button>
      </div>
      <div id="proj-dir-browser" style="display:none">
        <div class="dir-current" id="proj-dir-current"></div>
        <div class="dir-browser" id="proj-dirs"></div>
      </div>
      <label for="proj-agent">Agent</label>
      <input id="proj-agent" type="text" value="claude" placeholder="claude">
      <label for="proj-sysprompt">System Prompt</label>
      <textarea id="proj-sysprompt" rows="2" placeholder="(optional)"></textarea>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" id="proj-submit">Create</button>
      </div>
    `);

    document.getElementById('proj-browse-btn').addEventListener('click', () => {
      const mp = document.getElementById('proj-mp').value;
      if (!mp) { alert('Select a moneypenny first to browse paths'); return; }
      projFormState.selectedMP = mp;
      if (!projFormState.currentPath) projFormState.currentPath = '~';
      document.getElementById('proj-dir-browser').style.display = 'block';
      loadProjDirBrowser();
    });

    document.getElementById('proj-submit').addEventListener('click', submitCreateProject);
  }

  async function loadProjDirBrowser() {
    const dirsEl = document.getElementById('proj-dirs');
    const currentEl = document.getElementById('proj-dir-current');
    dirsEl.innerHTML = '<div class="loading">Loading...</div>';
    currentEl.textContent = '📂 ' + projFormState.currentPath;

    try {
      const resp = await apiCall('list-directory', '', ['-m', projFormState.selectedMP, '--path', projFormState.currentPath]);
      if (resp.status === 'ok' && resp.data) {
        projFormState.currentPath = resp.data.path || projFormState.currentPath;
        currentEl.textContent = '📂 ' + projFormState.currentPath;
        document.getElementById('proj-path').value = projFormState.currentPath;
        const entries = (resp.data.entries || []).filter(e => e.is_dir);
        let html = '<div class="dir-entry" data-path="..">📁 ..</div>';
        for (const entry of entries) {
          html += `<div class="dir-entry" data-path="${escapeAttr(entry.name)}">📁 ${escapeHtml(entry.name)}</div>`;
        }
        dirsEl.innerHTML = html;
        dirsEl.querySelectorAll('.dir-entry').forEach(el => {
          el.addEventListener('click', () => {
            const name = el.dataset.path;
            if (name === '..') {
              const parts = projFormState.currentPath.split('/');
              parts.pop();
              projFormState.currentPath = parts.join('/') || '/';
            } else {
              projFormState.currentPath = projFormState.currentPath.replace(/\/$/, '') + '/' + name;
            }
            loadProjDirBrowser();
          });
        });
      } else {
        const errMsg = resp.message ? escapeHtml(resp.message) : 'Could not list directory';
        dirsEl.innerHTML = `<div class="empty-state">${errMsg}</div>`;
      }
    } catch (e) {
      dirsEl.innerHTML = `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  async function submitCreateProject() {
    const name = document.getElementById('proj-name').value.trim();
    if (!name) { alert('Name is required'); return; }
    const args = ['--name', name];
    const mp = document.getElementById('proj-mp').value;
    if (mp) args.push('-m', mp);
    const path = document.getElementById('proj-path').value.trim();
    if (path) args.push('--path', path);
    const agent = document.getElementById('proj-agent').value.trim();
    if (agent) args.push('--agent', agent);
    const sysprompt = document.getElementById('proj-sysprompt').value.trim();
    if (sysprompt) args.push('--system-prompt', sysprompt);

    const btn = document.getElementById('proj-submit');
    btn.disabled = true;
    btn.textContent = 'Creating...';
    try {
      const resp = await apiCall('create', 'project', args);
      if (resp.status === 'error') {
        alert('Error: ' + resp.message);
        btn.disabled = false;
        btn.textContent = 'Create';
        return;
      }
      closeWizard();
      loadProjectsList();
    } catch (e) {
      alert('Error: ' + e.message);
      btn.disabled = false;
      btn.textContent = 'Create';
    }
  }

  async function showEditProjectModal(name) {
    const proj = projectsCache.find(p => p.name === name);
    projFormState = { selectedMP: proj ? proj.moneypenny : '', currentPath: proj ? proj.paths : '', moneypennies: [] };

    // Load moneypennies for dropdown.
    try {
      const resp = await apiCall('list', 'moneypenny', []);
      if (resp.status === 'ok' && resp.data && resp.data.rows) {
        projFormState.moneypennies = resp.data.rows.map(r => ({ name: r[0], type: r[1], isDefault: r[3] === '*' }));
      }
    } catch (e) { /* ignore */ }

    const mpOpts = projFormState.moneypennies
      .map(m => `<option value="${escapeAttr(m.name)}"${m.name === projFormState.selectedMP ? ' selected' : ''}>${escapeHtml(m.name)} (${escapeHtml(m.type)})</option>`).join('');

    renderWizardModal(`
      <h3>Edit Project</h3>
      <label for="eproj-name">Name</label>
      <input id="eproj-name" type="text" value="${escapeAttr(proj ? proj.name : name)}">
      <label for="eproj-status">Status</label>
      <select id="eproj-status">
        <option value="active"${proj && proj.status === 'active' ? ' selected' : ''}>Active</option>
        <option value="paused"${proj && proj.status === 'paused' ? ' selected' : ''}>Paused</option>
        <option value="done"${proj && proj.status === 'done' ? ' selected' : ''}>Done</option>
      </select>
      <label for="eproj-mp">Default Moneypenny</label>
      <select id="eproj-mp"><option value="">(none)</option>${mpOpts}</select>
      <label>Path</label>
      <div style="display:flex;align-items:center;gap:8px;margin-bottom:4px">
        <input id="eproj-path" type="text" value="${escapeAttr(proj ? proj.paths : '')}" style="flex:1" readonly>
        <button class="btn-muted" id="eproj-browse-btn">Browse</button>
      </div>
      <div id="eproj-dir-browser" style="display:none">
        <div class="dir-current" id="eproj-dir-current"></div>
        <div class="dir-browser" id="eproj-dirs"></div>
      </div>
      <label for="eproj-agent">Agent</label>
      <input id="eproj-agent" type="text" value="${escapeAttr(proj ? proj.agent : '')}">
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" id="eproj-submit">Save</button>
      </div>
    `);

    document.getElementById('eproj-browse-btn').addEventListener('click', () => {
      const mp = document.getElementById('eproj-mp').value;
      if (!mp) { alert('Select a moneypenny first to browse paths'); return; }
      projFormState.selectedMP = mp;
      if (!projFormState.currentPath) projFormState.currentPath = '~';
      document.getElementById('eproj-dir-browser').style.display = 'block';
      loadEditProjDirBrowser();
    });

    document.getElementById('eproj-submit').addEventListener('click', async () => {
      const args = [name];
      const newName = document.getElementById('eproj-name').value.trim();
      if (newName && newName !== name) args.push('--name', newName);
      const status = document.getElementById('eproj-status').value;
      if (proj && status !== proj.status) args.push('--status', status);
      const mp = document.getElementById('eproj-mp').value;
      if (proj && mp !== proj.moneypenny) args.push('-m', mp);
      const agent = document.getElementById('eproj-agent').value.trim();
      if (proj && agent !== proj.agent) args.push('--agent', agent);
      const path = document.getElementById('eproj-path').value.trim();
      if (proj && path !== proj.paths) args.push('--path', path);

      if (args.length <= 1) { closeWizard(); return; }

      const btn = document.getElementById('eproj-submit');
      btn.disabled = true;
      btn.textContent = 'Saving...';
      try {
        const resp = await apiCall('update', 'project', args);
        if (resp.status === 'error') {
          alert('Error: ' + resp.message);
          btn.disabled = false;
          btn.textContent = 'Save';
          return;
        }
        closeWizard();
        await loadProjects();
        loadProjectsList();
      } catch (e) {
        alert('Error: ' + e.message);
        btn.disabled = false;
        btn.textContent = 'Save';
      }
    });
  }

  async function loadEditProjDirBrowser() {
    const dirsEl = document.getElementById('eproj-dirs');
    const currentEl = document.getElementById('eproj-dir-current');
    dirsEl.innerHTML = '<div class="loading">Loading...</div>';
    currentEl.textContent = '📂 ' + projFormState.currentPath;

    try {
      const resp = await apiCall('list-directory', '', ['-m', projFormState.selectedMP, '--path', projFormState.currentPath]);
      if (resp.status === 'ok' && resp.data) {
        projFormState.currentPath = resp.data.path || projFormState.currentPath;
        currentEl.textContent = '📂 ' + projFormState.currentPath;
        document.getElementById('eproj-path').value = projFormState.currentPath;
        const entries = (resp.data.entries || []).filter(e => e.is_dir);
        let html = '<div class="dir-entry" data-path="..">📁 ..</div>';
        for (const entry of entries) {
          html += `<div class="dir-entry" data-path="${escapeAttr(entry.name)}">📁 ${escapeHtml(entry.name)}</div>`;
        }
        dirsEl.innerHTML = html;
        dirsEl.querySelectorAll('.dir-entry').forEach(el => {
          el.addEventListener('click', () => {
            const entryName = el.dataset.path;
            if (entryName === '..') {
              const parts = projFormState.currentPath.split('/');
              parts.pop();
              projFormState.currentPath = parts.join('/') || '/';
            } else {
              projFormState.currentPath = projFormState.currentPath.replace(/\/$/, '') + '/' + entryName;
            }
            loadEditProjDirBrowser();
          });
        });
      } else {
        const errMsg = resp.message ? escapeHtml(resp.message) : 'Could not list directory';
        dirsEl.innerHTML = `<div class="empty-state">${errMsg}</div>`;
      }
    } catch (e) {
      dirsEl.innerHTML = `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  // --- Traits Management ---

  function showTraitsView() {
    document.getElementById('dashboard-view').style.display = 'none';
    document.getElementById('traits-view').style.display = 'flex';
    stopDashboardPoll();
    mgmtCursor = 0;
    loadTraitsList();
  }

  function hideTraitsView() {
    document.getElementById('traits-view').style.display = 'none';
    document.getElementById('dashboard-view').style.display = 'flex';
    loadTraitsCache(); // refresh cache
    loadDashboard();
    startDashboardPoll();
  }

  async function loadTraitsList() {
    const container = document.getElementById('trait-content');
    container.innerHTML = '<div class="loading">Loading...</div>';
    try {
      const resp = await apiCall('list', 'trait', []);
      if (resp.status === 'error') {
        container.innerHTML = `<div class="empty-state">Error: ${escapeHtml(resp.message)}</div>`;
        return;
      }
      const rows = (resp.data && resp.data.rows) || [];
      traitsCache = rows.map(r => ({ id: r[0], name: r[1], preview: r[2], def: r[3] === 'yes' }));
      if (rows.length === 0) {
        container.innerHTML = '<div class="empty-state">No traits. Create one to get started.</div>';
        return;
      }
      let html = '';
      for (const r of rows) {
        const id = r[0], name = r[1], preview = r[2], def = r[3] === 'yes';
        html += `
          <div class="mgmt-row">
            <span class="mgmt-name">${escapeHtml(name)}${def ? ' <span title="Enabled by default for new agents" style="color:var(--accent)">✔</span>' : ''}</span>
            <span class="mgmt-detail" style="flex:1">${escapeHtml(preview)}</span>
            <span class="mgmt-actions">
              <button data-trait-id="${escapeAttr(id)}" data-action="edit">Edit</button>
              <button data-trait-id="${escapeAttr(id)}" data-trait-name="${escapeAttr(name)}" data-action="delete" class="danger">Delete</button>
            </span>
          </div>`;
      }
      container.innerHTML = html;
      container.querySelectorAll('.mgmt-actions button').forEach(btn => {
        btn.addEventListener('click', (e) => {
          e.stopPropagation();
          const id = btn.dataset.traitId;
          const action = btn.dataset.action;
          if (action === 'edit') showEditTraitModal(id);
          else if (action === 'delete') deleteTrait(id, btn.dataset.traitName);
        });
      });
      applyMgmtCursor(container, false);
    } catch (e) {
      container.innerHTML = `<div class="empty-state">Error: ${escapeHtml(e.message)}</div>`;
    }
  }

  async function deleteTrait(id, name) {
    if (!confirm('Delete trait "' + (name || id) + '"?')) return;
    try {
      const resp = await apiCall('delete', 'trait', [id]);
      if (resp.status === 'error') alert('Error: ' + resp.message);
      else loadTraitsList();
    } catch (e) { alert('Error: ' + e.message); }
  }

  function showCreateTraitModal() {
    renderWizardModal(`
      <h3>New Trait</h3>
      <label for="trait-name">Name *</label>
      <input id="trait-name" type="text" placeholder="e.g. Concise commits">
      <label for="trait-prompt">Prompt</label>
      <textarea id="trait-prompt" rows="6" placeholder="System-prompt snippet describing how the agent should behave..."></textarea>
      <div class="toggle-row"><input type="checkbox" id="trait-default"><label for="trait-default" style="margin:0;color:var(--text)">Enable by default for new agents</label></div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" id="trait-submit">Create</button>
      </div>
    `);
    document.getElementById('trait-submit').addEventListener('click', async () => {
      const name = document.getElementById('trait-name').value.trim();
      if (!name) { alert('Name is required'); return; }
      const prompt = document.getElementById('trait-prompt').value;
      const def = document.getElementById('trait-default').checked;
      const btn = document.getElementById('trait-submit');
      btn.disabled = true; btn.textContent = 'Creating...';
      try {
        const resp = await apiCall('create', 'trait', ['--name', name, '--prompt', prompt, `--default=${def}`]);
        if (resp.status === 'error') {
          alert('Error: ' + resp.message);
          btn.disabled = false; btn.textContent = 'Create';
          return;
        }
        closeWizard();
        await loadTraitsCache();
        loadTraitsList();
      } catch (e) {
        alert('Error: ' + e.message);
        btn.disabled = false; btn.textContent = 'Create';
      }
    });
  }

  async function showEditTraitModal(id) {
    renderWizardModal(`<h3>Edit Trait</h3><div class="loading">Loading...</div>`);
    let t;
    try {
      const resp = await apiCall('show', 'trait', [id]);
      if (resp.status === 'error') {
        renderWizardModal(`<h3>Edit Trait</h3><div class="empty-state">Error: ${escapeHtml(resp.message)}</div>
          <div class="modal-actions"><button class="btn-muted" onclick="window._qewCloseWizard()">Close</button></div>`);
        return;
      }
      t = resp.data || {};
    } catch (e) {
      renderWizardModal(`<h3>Edit Trait</h3><div class="empty-state">Error: ${escapeHtml(e.message)}</div>
        <div class="modal-actions"><button class="btn-muted" onclick="window._qewCloseWizard()">Close</button></div>`);
      return;
    }
    renderWizardModal(`
      <h3>Edit Trait</h3>
      <label for="trait-name">Name</label>
      <input id="trait-name" type="text" value="${escapeAttr(t.name || '')}">
      <label for="trait-prompt">Prompt</label>
      <textarea id="trait-prompt" rows="6">${escapeHtml(t.prompt || '')}</textarea>
      <div class="toggle-row"><input type="checkbox" id="trait-default"${t.enabled_by_default ? ' checked' : ''}><label for="trait-default" style="margin:0;color:var(--text)">Enable by default for new agents</label></div>
      <div class="modal-actions">
        <button class="btn-muted" onclick="window._qewCloseWizard()">Cancel</button>
        <button class="btn" id="trait-submit">Save</button>
      </div>
    `);
    document.getElementById('trait-submit').addEventListener('click', async () => {
      const args = [id];
      const name = document.getElementById('trait-name').value.trim();
      if (name !== (t.name || '')) args.push('--name', name);
      const prompt = document.getElementById('trait-prompt').value;
      if (prompt !== (t.prompt || '')) args.push('--prompt', prompt);
      const def = document.getElementById('trait-default').checked;
      if (def !== !!t.enabled_by_default) args.push(`--default=${def}`);
      if (args.length <= 1) { closeWizard(); return; }
      const btn = document.getElementById('trait-submit');
      btn.disabled = true; btn.textContent = 'Saving...';
      try {
        const resp = await apiCall('update', 'trait', args);
        if (resp.status === 'error') {
          alert('Error: ' + resp.message);
          btn.disabled = false; btn.textContent = 'Save';
          return;
        }
        closeWizard();
        await loadTraitsCache();
        loadTraitsList();
      } catch (e) {
        alert('Error: ' + e.message);
        btn.disabled = false; btn.textContent = 'Save';
      }
    });
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

  function relativeTime(ts) {
    if (!ts) return '';
    // Try parsing "Jan 02 15:04" (add current year) or ISO format.
    let d;
    const isoMatch = ts.match(/^\d{4}-\d{2}-\d{2}/);
    if (isoMatch) {
      d = new Date(ts);
    } else {
      // "Jan 02 15:04" format — add current year.
      d = new Date(ts + ' ' + new Date().getFullYear());
      if (isNaN(d.getTime())) return ts;
      // If parsed date is more than 1 day in the future, it's probably last year.
      if (d > new Date(Date.now() + 86400000)) {
        d = new Date(ts + ' ' + (new Date().getFullYear() - 1));
      }
    }
    if (isNaN(d.getTime())) return ts;
    const secs = (Date.now() - d.getTime()) / 1000;
    if (secs < 60) return 'just now';
    if (secs < 3600) return Math.floor(secs / 60) + 'm ago';
    if (secs < 86400) return Math.floor(secs / 3600) + 'h ago';
    if (secs < 604800) return Math.floor(secs / 86400) + 'd ago';
    return d.toLocaleDateString('en-US', { month: 'short', day: '2-digit' });
  }

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
    // Italic: *...* or _..._ (after bold so ** is already consumed). Requires
    // non-word boundaries and non-space inner edges so snake_case identifiers
    // and arithmetic like "2 * 3" are left untouched.
    html = html.replace(/(^|[^\w*])\*(\S|\S[^*\n]*?\S)\*(?!\w)/g, '$1<em>$2</em>');
    html = html.replace(/(^|[^\w_])_(\S|\S[^_\n]*?\S)_(?!\w)/g, '$1<em>$2</em>');
    // Links: [text](url) — opens in new tab
    html = html.replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    // Bare URLs: https://... or http://... (not already inside a tag)
    html = html.replace(/(^|[^"=])((https?:\/\/)[^\s<]+)/g, '$1<a href="$2" target="_blank" rel="noopener">$2</a>');
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
      new Audio('james.wav').play();
    } catch (e) { /* ignore audio errors */ }
  }

  function showNotification(sessionName, sessionId) {
    playNotificationSound();
    // Pop-over notification — clickable to open session.
    const popover = document.createElement('div');
    popover.className = 'notification-popover';
    popover.textContent = 'Session ready: ' + sessionName;
    if (sessionId) {
      popover.addEventListener('click', () => {
        popover.remove();
        openChat(sessionId, sessionName);
      });
    }
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

  // toggleThoughts shows/hides persisted train-of-thought turns in the open chat.
  function toggleThoughts() {
    showThoughts = !showThoughts;
    localStorage.setItem('qewShowThoughts', showThoughts ? '1' : '0');
    syncThoughtsToggle();
    lastChatHTML = ''; // force re-render
    renderChat();
  }

  function syncThoughtsToggle() {
    const btn = document.getElementById('thoughts-toggle');
    if (!btn) return;
    btn.textContent = showThoughts ? '💭' : '💤';
    btn.title = showThoughts ? 'Train of thought shown (click to hide) — T'
                             : 'Train of thought hidden (click to show) — T';
    btn.classList.toggle('active', showThoughts);
  }

  // --- Init ---

  document.getElementById('chat-back').addEventListener('click', closeChat);
  document.getElementById('chat-send').addEventListener('click', sendMessage);
  document.getElementById('sound-toggle').addEventListener('click', toggleSound);
  document.getElementById('passkey-mgmt-btn').addEventListener('click', openPasskeyModal);
  document.getElementById('thoughts-toggle').addEventListener('click', toggleThoughts);
  syncThoughtsToggle();
  document.getElementById('new-session-btn').addEventListener('click', openCreateWizard);
  document.getElementById('nav-moneypennies-btn').addEventListener('click', showMoneypenniesView);
  document.getElementById('nav-projects-btn').addEventListener('click', showProjectsView);
  document.getElementById('nav-traits-btn').addEventListener('click', showTraitsView);
  document.getElementById('mp-back-btn').addEventListener('click', hideMoneypenniesView);
  document.getElementById('mp-add-btn').addEventListener('click', showAddMoneypennyModal);
  document.getElementById('proj-back-btn').addEventListener('click', hideProjectsView);
  document.getElementById('proj-add-btn').addEventListener('click', showCreateProjectModal);
  document.getElementById('trait-back-btn').addEventListener('click', hideTraitsView);
  document.getElementById('trait-add-btn').addEventListener('click', showCreateTraitModal);

  // Actions dropdown menu.
  const menuBtn = document.getElementById('chat-menu-btn');
  const menu = document.getElementById('chat-menu');
  menuBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    menu.classList.toggle('open');
  });
  document.addEventListener('click', () => menu.classList.remove('open'));
  menu.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    menu.classList.remove('open');
    const action = btn.dataset.action;
    if (action === 'diff') showDiff();
    else if (action === 'git-log') showGitLog();
    else if (action === 'commit') showCommitModal(false);
    else if (action === 'commit-push') showCommitModal(true);
    else if (action === 'branch') showBranchModal();
    else if (action === 'push') gitPush();
    else if (action === 'new-subagent') createNewSubagent();
    else if (action === 'memory') openMemoryModal();
    else if (action === 'compact') compactSession();
    else if (action === 'distill') distillSession();
    else if (action === 'duplicate') openDuplicateWizard();
    else if (action === 'edit') showEditSessionModal();
    else if (action === 'move-project') showMoveToProjectModal();
    else if (action === 'stop') stopSession();
    else if (action === 'complete') completeSession();
    else if (action === 'delete') deleteSession();
  });

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

  // Override dropdowns: store the temporary override and refocus the input.
  const modelOverrideSel = document.getElementById('chat-model-override');
  if (modelOverrideSel) {
    modelOverrideSel.addEventListener('change', (e) => {
      overrideModel = e.target.value;
      chatInput.focus();
    });
  }
  const effortOverrideSel = document.getElementById('chat-effort-override');
  if (effortOverrideSel) {
    effortOverrideSel.addEventListener('change', (e) => {
      overrideEffort = e.target.value;
      chatInput.focus();
    });
  }

  // Load older history when the user scrolls to the top of the chat.
  const chatMessagesEl = document.getElementById('chat-messages');
  chatMessagesEl.addEventListener('scroll', () => {
    if (chatMessagesEl.scrollTop < 60 && !chatLoadingMore && chatConversation.length < chatTotal) {
      loadOlderHistory();
    }
  });

  // --- Keyboard navigation ---

  // Escape inside a modal triggers its Close/Cancel/Back/OK button so the
  // button's own logic (e.g. the diff review's unsaved-comment confirmation)
  // still runs. Returns true if a button was clicked.
  function escapeCloseModal() {
    const overlay = document.querySelector('.modal-overlay');
    if (!overlay) return false;
    const labels = ['close', 'cancel', 'back', 'ok'];
    const pick = (root) => {
      const btns = Array.from(root.querySelectorAll('button'));
      for (const label of labels) {
        const btn = btns.find(b => b.textContent.trim().toLowerCase() === label);
        if (btn) return btn;
      }
      return null;
    };
    // If focus is in the inline diff comment editor, cancel just that editor
    // rather than discarding the whole review.
    const active = document.activeElement;
    if (active) {
      const editor = active.closest('.diff-comment-editor');
      if (editor && overlay.contains(editor)) {
        const btn = pick(editor);
        if (btn) { btn.click(); return true; }
      }
    }
    // Otherwise click the modal's primary action-row close button.
    const modal = overlay.querySelector('.modal');
    if (modal) {
      const rows = modal.querySelectorAll(':scope > .modal-actions');
      for (const row of rows) {
        const btn = pick(row);
        if (btn) { btn.click(); return true; }
      }
    }
    return false;
  }

  // Cmd/Ctrl+Enter inside a modal triggers its primary call-to-action button
  // (the single non-muted .btn in the modal's action row), mirroring Escape's
  // dismiss behaviour. Does nothing when the CTA is ambiguous (zero or several
  // primary buttons, e.g. the git diff view's multiple actions). Returns true
  // if a button was clicked.
  function submitModalCTA() {
    const overlay = document.querySelector('.modal-overlay');
    if (!overlay) return false;
    const modal = overlay.querySelector('.modal');
    if (!modal) return false;
    const rows = modal.querySelectorAll(':scope > .modal-actions');
    for (const row of rows) {
      const btns = Array.from(row.querySelectorAll('button.btn:not(.btn-muted)'))
        .filter(b => b.offsetParent !== null && !b.disabled);
      if (btns.length === 1) { btns[0].click(); return true; }
    }
    return false;
  }

  document.addEventListener('keydown', (e) => {
    // Ignore IME composition and auto-repeat for shortcut handling.
    if (e.isComposing) return;

    // The in-conversation command palette captures single-key actions while open.
    if (cmdPaletteOpen && document.querySelector('.cmd-palette')) {
      if (e.key === 'Escape') { e.preventDefault(); closeCmdPalette(true); return; }
      if (e.ctrlKey || e.metaKey || e.altKey || e.repeat) return;
      const map = { c: 'complete', e: 'edit', y: 'duplicate', g: 'diff', t: 'thoughts', o: 'model', f: 'effort', m: 'memory', K: 'compact', D: 'distill', s: 'stop', d: 'delete', q: 'back' };
      const cmd = map[e.key];
      if (cmd) { e.preventDefault(); runCmd(cmd); }
      return;
    }

    // While any other modal is open, Escape triggers its close button; the git
    // diff review modal additionally supports keyboard navigation. All other
    // keys are left to the modal's own handlers.
    if (document.querySelector('.modal-overlay')) {
      if (e.key === 'Escape') {
        if (escapeCloseModal()) e.preventDefault();
        return;
      }
      // Cmd/Ctrl+Enter submits the modal's primary action. The diff comment
      // editor has its own Cmd/Ctrl+Enter (save comment), so defer to it when
      // focus is inside that editor.
      if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
        const active = document.activeElement;
        if (active && active.closest('.diff-comment-editor')) return;
        if (submitModalCTA()) e.preventDefault();
        return;
      }
      if (handleDiffModalKey(e)) return;
      handleWizardListKey(e);
      return;
    }

    const chatActive = currentSession &&
      document.getElementById('chat-view').style.display !== 'none';

    if (chatActive) {
      if (e.key === 'Escape') { e.preventDefault(); openCmdPalette(); return; }
      if ((e.ctrlKey || e.metaKey) && (e.key === 'd' || e.key === 'D')) {
        e.preventDefault(); chatScroll(1); return;
      }
      if ((e.ctrlKey || e.metaKey) && (e.key === 'u' || e.key === 'U')) {
        e.preventDefault(); chatScroll(-1); return;
      }
      return;
    }

    // Moneypennies / traits management list views: list navigation + shortcuts.
    const mgmt = activeMgmtList();
    if (mgmt) {
      if (e.ctrlKey || e.metaKey || e.altKey || e.repeat) return;
      const tag = (e.target.tagName || '').toLowerCase();
      if (tag === 'input' || tag === 'textarea' || tag === 'select' || e.target.isContentEditable) return;
      if (e.key === 'Escape') {
        e.preventDefault();
        if (mgmt.kind === 'mp') hideMoneypenniesView(); else hideTraitsView();
        return;
      }
      if (e.key === 'ArrowDown' || e.key === 'j') { e.preventDefault(); mgmtMove(1); return; }
      if (e.key === 'ArrowUp' || e.key === 'k') { e.preventDefault(); mgmtMove(-1); return; }
      // Let Enter activate a focused row/toolbar button natively (e.g. after a
      // mouse click) instead of hijacking it for the selected-row action.
      if (e.key === 'Enter' && (tag === 'button' || tag === 'a')) return;
      if (mgmtAction(e.key)) e.preventDefault();
      return;
    }

    // Dashboard list navigation + shortcuts (only when the dashboard is the
    // active view and focus is not in a form field).
    const dashActive = document.getElementById('dashboard-view').style.display !== 'none';
    if (!dashActive || e.ctrlKey || e.metaKey || e.altKey || e.repeat) return;
    const tag = (e.target.tagName || '').toLowerCase();
    const typingText = tag === 'input' || tag === 'textarea' || tag === 'select' ||
      e.target.isContentEditable;
    const typingFocus = typingText || tag === 'button' || tag === 'a';

    // Enter activates the selected row, but only when focus isn't on a button/link
    // (so toolbar buttons keep their native Enter behaviour).
    if (e.key === 'Enter') {
      if (!typingFocus) { e.preventDefault(); dashOpenSelected(); }
      return;
    }
    if (typingText) return;
    switch (e.key) {
      case 'ArrowDown': case 'j': e.preventDefault(); dashMove(1); break;
      case 'ArrowUp':   case 'k': e.preventDefault(); dashMove(-1); break;
      case '/': e.preventDefault(); openDashFilter(); break;
      case 'm': e.preventDefault(); showMoneypenniesView(); break;
      case 'b': e.preventDefault(); toggleSound(); break;
      case 'n': e.preventDefault(); openCreateWizard(); break;
      case 'p': e.preventDefault(); showProjectsView(); break;
      case 't': e.preventDefault(); showTraitsView(); break;
      case 'c': e.preventDefault(); dashAction('complete'); break;
      case 'd': e.preventDefault(); dashAction('delete'); break;
      case 'e': e.preventDefault(); dashAction('edit'); break;
      case 'y': e.preventDefault(); dashAction('duplicate'); break;
    }
  });

  // --- Routing ---

  function handleRoute() {
    const hash = window.location.hash;
    const match = hash.match(/^#\/session\/(.+)$/);
    if (match) {
      const sessionId = match[1];
      // If already viewing this session, do nothing.
      if (currentSession === sessionId) return;
      openChat(sessionId);
    } else if (currentSession) {
      closeChat();
    }
  }

  window.addEventListener('hashchange', handleRoute);

  initDashFilter();

  // Display the Qew version in the header.
  fetch('/version').then(r => r.ok ? r.text() : '').then(v => {
    if (v) {
      const el = document.getElementById('app-version');
      if (el) el.textContent = 'v' + v.trim();
    }
  }).catch(() => {});

  // Initial load.
  Promise.all([loadProjects(), loadTraitsCache(), loadDashboard()]).then(() => {
    document.getElementById('conn-status').innerHTML =
      '<span class="status-dot connected"></span>Connected';
    startDashboardPoll();
    // Check initial hash route after dashboard is loaded.
    handleRoute();
  }).catch(() => {
    document.getElementById('conn-status').innerHTML =
      '<span class="status-dot disconnected"></span>Disconnected';
  });
})();
