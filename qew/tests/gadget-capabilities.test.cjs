const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const controls = require('../pkg/web/static/gadget-capabilities.js');
const thresholds = require('../pkg/web/static/compaction-threshold.js');

const defaults = { memory: true, subagents: true, agents: false, create_agents: false, edit_sessions: false, edit_own_session: false, edit_own_subagents: false, moneypenny_logs: false, hem: false, traits: false, scheduling: true };
const allOff = Object.fromEntries(Object.keys(defaults).map(name => [name, false]));
const documentFor = (values) => ({
  getElementById(id) {
    return { checked: values[id.split('-').at(-1)] };
  },
});

test('shared-memory forms preserve read-only assignments and explicit opt-outs', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  const context = vm.createContext({});
  vm.runInContext(app.slice(app.indexOf('  function sharedMemorySpec('), app.indexOf('  async function openCreateWizard(')), context);
  assert.equal(context.sharedMemorySpec([{ id: 'engineering', publish: true }, { id: 'reference', publish: false }]), 'engineering,reference:read');
  assert.equal(context.sharedMemorySpec([]), '');
  assert.equal(context.sharedMemorySpec(undefined), '');
  assert.match(app, /args.push\('--ponsonbies', memories \|\| 'none'\)/);
  assert.match(app, /populatePonsonbySelector\('es', s.ponsonbies, false\)/);
  assert.match(app, /Publishing stores receive unprocessed conversation history/);
});

test('Ponsonby assignment selectors preserve defaults, read-only access and discovery errors', async () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  let choices = [], retry, fail = false;
  const container = {
    dataset: {},
    set innerHTML(html) {
      this.html = html;
      choices = Array.from(html.matchAll(/<select[^>]+data-ponsonby="([^"]+)"[^>]*>([\s\S]*?)<\/select>/g))
        .map(([, id, options]) => ({ dataset: { ponsonby: id }, value: options.match(/value="([^"]+)" selected/)[1] }));
    },
    querySelectorAll: () => choices,
    querySelector: () => ({ addEventListener: (_, action) => { retry = action; } }),
  };
  const escape = value => String(value).replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
  const context = vm.createContext({
    document: { getElementById: () => container },
    escapeHtml: escape, escapeAttr: escape,
    apiCall: async () => fail ? { status: 'error', message: 'unreachable' } :
      { status: 'ok', data: { rows: [['one', '<Engineering>', 'Decisions', 'host', 'yes'], ['two', 'Reference', 'Docs', 'host', 'no']] } },
  });
  vm.runInContext(app.slice(app.indexOf('  function sharedMemorySpec('), app.indexOf('  async function openCreateWizard(')), context);
  assert.throws(() => context.selectedPonsonbies('wiz'), /Wait for available/);
  await context.populatePonsonbySelector('wiz', [], true);
  assert.match(container.html, /&lt;Engineering&gt;/);
  assert.equal(context.selectedPonsonbies('wiz'), 'one');
  choices[0].value = 'off';
  assert.equal(context.selectedPonsonbies('wiz'), '');
  await context.populatePonsonbySelector('es', [{ id: 'two', publish: false }, { id: 'legacy', name: 'Old store', publish: true }], false);
  assert.equal(context.selectedPonsonbies('es'), 'two:read,legacy');
  assert.match(container.html, /Currently assigned; unavailable/);
  fail = true;
  await context.populatePonsonbySelector('es', [], false);
  assert.match(container.html, /Could not load Ponsonbies: unreachable/);
  assert.throws(() => context.selectedPonsonbies('es'), /Wait for available/);
  fail = false;
  await retry();
  assert.equal(context.selectedPonsonbies('es'), '');
});

test('Ponsonby management creates, edits, retries and deletes through existing modals', async () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  const html = fs.readFileSync(path.join(__dirname, '../pkg/web/static/index.html'), 'utf8');
  assert.match(html, /id="nav-ponsonbies-btn"/);
  assert.match(html, /id="ponsonbies-view"/);
  const p = { id: 'store', name: 'engineering', purpose: '<script>shared</script>', moneypenny: 'host', agent: 'copilot', model: 'cheap', default: true,
    introduction: '<private>', jobs: [{ id: 'failed-request', kind: 'ingest', status: 'failed', error: '<unavailable>' }] };
  const events = [], errors = [], buttons = {};
  const nodes = {
    'ponsonby-content': { querySelectorAll: () => [] },
    'pn-name': { value: 'engineering' }, 'pn-purpose': { value: 'Shared decisions' },
    'pn-host': { value: 'host', addEventListener: (_, action) => { buttons.hostChange = action; } },
    'pn-agent': { value: 'copilot', addEventListener: (_, action) => { buttons.agentChange = action; } }, 'pn-model': { value: 'cheap' },
    'pn-model-status': {},
    'pn-default': { checked: false },
  };
  const button = id => buttons[id] ||= { disabled: false, addEventListener: (_, action) => { buttons[id].click = action; } };
  let markup = '', failure = false, confirmed = true, modelFailure = false;
  const retry = { dataset: { request: 'failed-request' }, addEventListener: (_, action) => { retry.click = action; } };
  const escape = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
  const context = vm.createContext({
    document: {
      getElementById: id => nodes[id] || button(id),
      querySelectorAll: () => [retry],
    },
    escapeHtml: escape, escapeAttr: escape,
    modelOptionsHtml: (models, selected) => `<option value="">Select</option>${models.map(m => `<option>${m}</option>`).join('')}${selected && !models.includes(selected) ? `<option selected>${selected}</option>` : ''}`,
    modelLoadErrorOption: e => `<option disabled>${escape(e.message)}</option>`,
    loadModels: async (host, agent) => { events.push(['models', host, agent]); if (modelFailure) throw new Error('discovery failed'); return ['cheap', 'other-model']; },
    applyMgmtCursor: () => {},
    renderWizardModal: content => { markup = content; events.push(['modal']); },
    closeWizard: () => events.push(['closed']),
    alert: message => errors.push(message),
    confirm: () => confirmed,
    apiCall: async (verb, noun, args) => {
      events.push([verb, noun, Array.from(args)]);
      if (verb === 'list' && noun === 'moneypenny') return { status: 'ok', data: { rows: [['host', 'local', '', '*']] } };
      if (verb === 'list') return { status: 'ok', data: { rows: [[p.id, p.name, p.purpose, p.moneypenny, 'yes', p.agent, p.model]] } };
      if (verb === 'show') return { status: 'ok', data: p };
      return failure ? { status: 'error', message: 'host unavailable' } : { status: 'ok' };
    },
  });
  vm.runInContext(app.slice(app.indexOf('  // --- Ponsonby Management ---'), app.indexOf('  // --- Moneypennies Management ---')), context);
  await context.loadPonsonbies();
  assert.match(nodes['ponsonby-content'].innerHTML, /&lt;script&gt;shared&lt;\/script&gt;/);
  await context.showPonsonbyForm();
  assert.match(markup, /<select id="pn-model">/);
  assert.ok(events.some(e => e[0] === 'models' && e[1] === 'host' && e[2] === 'copilot'));
  nodes['pn-model'].value = 'cheap';
  assert.match(markup, /Assign to new agents by default/);
  assert.match(markup, /configured LLM provider/);
  await buttons['pn-save'].click();
  let request = events.find(e => e[0] === 'create');
  assert.deepEqual(request, ['create', 'ponsonby', ['--name', 'engineering', '--moneypenny', 'host', '--purpose', 'Shared decisions', '--agent', 'copilot', '--model', 'cheap', '--default=false']]);
  assert.ok(events.some(e => e[0] === 'closed'));
  await context.showPonsonbyForm('store');
  assert.match(markup, /id="pn-name"[^>]+disabled/);
  assert.match(markup, /id="pn-host"[^>]+disabled/);
  nodes['pn-agent'].value = 'claude';
  await buttons.agentChange();
  assert.equal(nodes['pn-model'].value, '');
  assert.ok(events.some(e => e[0] === 'models' && e[2] === 'claude'));
  nodes['pn-agent'].value = 'copilot';
  await buttons.agentChange();
  nodes['pn-model'].value = 'cheap';
  modelFailure = true;
  await buttons['pn-model-refresh'].click();
  assert.equal(nodes['pn-model'].value, 'cheap');
  assert.match(nodes['pn-model-status'].textContent, /discovery failed/);
  assert.equal(nodes['pn-model'].disabled, false);
  modelFailure = false;
  nodes['pn-host'].value = 'remote';
  await buttons.hostChange();
  assert.equal(nodes['pn-model'].value, '');
  assert.ok(events.some(e => e[0] === 'models' && e[1] === 'remote'));
  nodes['pn-host'].value = 'host';
  nodes['pn-model'].value = 'cheap';
  const before = events.filter(e => e[0] === 'closed').length;
  failure = true;
  await buttons['pn-save'].click();
  assert.equal(buttons['pn-save'].disabled, false);
  assert.equal(events.filter(e => e[0] === 'closed').length, before);
  assert.equal(nodes['pn-purpose'].value, 'Shared decisions');
  assert.equal(errors.at(-1), 'Error: host unavailable');
  request = events.find(e => e[0] === 'update');
  assert.equal(request[2][0], 'store');
  assert.equal(request[2].includes('--name'), false);
  assert.equal(request[2].includes('--moneypenny'), false);
  failure = false;
  await context.showPonsonbyDetail('store');
  assert.match(markup, /&lt;private&gt;/);
  assert.match(markup, /&lt;unavailable&gt;/);
  await retry.click();
  assert.ok(events.some(e => e[0] === 'retry' && e[2].join(' ') === 'store --request failed-request'));
  await context.togglePonsonbyDefault('store');
  assert.ok(events.some(e => e[0] === 'update' && e[2].join(' ') === 'store --default=false'));
  confirmed = false;
  await context.deletePonsonby('store');
  assert.equal(events.some(e => e[0] === 'delete'), false);
  confirmed = true;
  failure = true;
  await context.deletePonsonby('store');
  assert.ok(events.some(e => e[0] === 'delete' && e[2][0] === 'store'));
  assert.equal(errors.at(-1), 'Error: host unavailable');
  const pending = [];
  context.loadModels = () => new Promise(resolve => pending.push(resolve));
  nodes['pn-host'].value = 'old-host';
  const oldDiscovery = buttons.hostChange();
  nodes['pn-host'].value = 'new-host';
  const newDiscovery = buttons.hostChange();
  pending[1](['new-host-model']);
  await newDiscovery;
  assert.match(nodes['pn-model'].innerHTML, /new-host-model/);
  pending[0](['stale-model']);
  await oldDiscovery;
  assert.doesNotMatch(nodes['pn-model'].innerHTML, /stale-model/);
  assert.equal(nodes['pn-model'].disabled, false);
});

test('Ponsonby list reuses management selection and keyboard actions', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  const calls = [];
  const content = { querySelectorAll: () => [{ dataset: { ponsonbyId: 'selected-store' } }] };
  const context = vm.createContext({
    mgmtCursor: 0,
    document: { getElementById: id => id === 'ponsonbies-view' ? { style: { display: 'flex' } } : id === 'ponsonby-content' ? content : null },
    showPonsonbyDetail: id => calls.push(['show', id]),
    showPonsonbyForm: id => calls.push(['form', id]),
    togglePonsonbyDefault: id => calls.push(['default', id]),
    deletePonsonby: id => calls.push(['delete', id]),
    loadPonsonbies: () => calls.push(['refresh']),
  });
  vm.runInContext(app.slice(app.indexOf('  function activeMgmtList('), app.indexOf('  // handleWizardListKey')), context);
  for (const key of ['Enter', 'e', 's', 'd', 'n', 'r']) assert.equal(context.mgmtAction(key), true);
  assert.deepEqual(calls, [['show', 'selected-store'], ['form', 'selected-store'], ['default', 'selected-store'], ['delete', 'selected-store'], ['form', undefined], ['refresh']]);
});

test('legacy details use defaults without replacing explicit false', () => {
  assert.deepEqual(controls.values(), defaults);
  assert.deepEqual(controls.values(null), defaults);
  assert.deepEqual(controls.values(allOff), allOff);
  assert.deepEqual(controls.values({ agents: true }), { ...defaults, agents: true });
});

test('create and copy emit all explicit permissions, including false', () => {
  assert.deepEqual(controls.args('wiz', undefined, documentFor(allOff)), [
    '--gadget-memory=false', '--gadget-subagents=false',
    '--gadget-agents=false', '--gadget-create-agents=false', '--gadget-edit-sessions=false', '--gadget-edit-own-session=false', '--gadget-edit-own-subagents=false',
    '--gadget-moneypenny-logs=false', '--gadget-hem=false', '--gadget-traits=false', '--gadget-scheduling=false',
  ]);
});

test('top-level creation is an independent opt-in grant', () => {
  const granted = { ...defaults, create_agents: true };
  assert.deepEqual(controls.values(granted), granted);
  assert.ok(controls.args('wiz', undefined, documentFor(granted)).includes('--gadget-create-agents=true'));
  assert.deepEqual(controls.args('es', defaults, documentFor(granted)), ['--gadget-create-agents=true']);
  assert.deepEqual(controls.args('es', granted, documentFor(defaults)), ['--gadget-create-agents=false']);
});

test('edit emits only changed settings and supports revocation', () => {
  assert.deepEqual(controls.args('es', null, documentFor(defaults)), []);
  assert.deepEqual(controls.args('es', defaults, documentFor({ ...defaults, memory: false })), [
    '--gadget-memory=false',
  ]);
  assert.deepEqual(controls.args('es', { ...defaults, agents: true }, documentFor(defaults)), [
    '--gadget-agents=false',
  ]);
});

test('traits opt-in survives copy and edit can grant or revoke it alone', () => {
  const granted = { ...defaults, traits: true };
  assert.deepEqual(controls.values(granted), granted);
  assert.ok(controls.args('wiz', undefined, documentFor(granted)).includes('--gadget-traits=true'));
  assert.deepEqual(controls.args('es', defaults, documentFor(granted)), ['--gadget-traits=true']);
  assert.deepEqual(controls.args('es', granted, documentFor(defaults)), ['--gadget-traits=false']);
});

test('daemon logs opt-in survives copy and can be granted or revoked alone', () => {
  const granted = { ...defaults, moneypenny_logs: true };
  assert.ok(controls.args('wiz', undefined, documentFor(granted)).includes('--gadget-moneypenny-logs=true'));
  assert.deepEqual(controls.args('es', defaults, documentFor(granted)), ['--gadget-moneypenny-logs=true']);
  assert.deepEqual(controls.args('es', granted, documentFor(defaults)), ['--gadget-moneypenny-logs=false']);
});

test('reusable controls render accessible toggles and permanent notifications', () => {
  const rendered = controls.render('wiz', defaults);
  for (const name of Object.keys(defaults)) {
    assert.ok(rendered.includes(`for="wiz-gadget-${name}"`));
    assert.ok(rendered.includes(`id="wiz-gadget-${name}"${defaults[name] ? ' checked' : ''}>`));
  }
  assert.equal((rendered.match(/type="checkbox"/g) || []).length, Object.keys(defaults).length);
  assert.match(rendered, /Notifications to you are always available/);
  assert.match(rendered, /Create, list, and message James child sessions; parent replies always allowed/);
  assert.match(rendered, /no management access/);
  assert.match(rendered, /shared trait bodies.*future use by all agents/);
  assert.match(rendered, /Hem administrative proxy.*mutations and permission changes/);
});

test('Hem administrative proxy can be granted and revoked independently', () => {
  const granted = { ...allOff, hem: true };
  assert.ok(controls.args('wiz', undefined, documentFor(granted)).includes('--gadget-hem=true'));
  assert.deepEqual(controls.args('es', allOff, documentFor(granted)), ['--gadget-hem=true']);
  assert.deepEqual(controls.args('es', granted, documentFor(allOff)), ['--gadget-hem=false']);
});

test('create, copy and edit surfaces load and use the reusable controls', () => {
  const staticPath = path.join(__dirname, '../pkg/web/static');
  const index = fs.readFileSync(path.join(staticPath, 'index.html'), 'utf8');
  assert.ok(index.indexOf('gadget-capabilities.js') < index.indexOf('src="app.js"'));
  const app = fs.readFileSync(path.join(staticPath, 'app.js'), 'utf8');
  assert.ok(app.includes("gadgetCapabilities.render('wiz', copy ? src.gadget_capabilities : undefined)"));
  assert.ok(app.includes("gadgetCapabilities.args('wiz')"));
  assert.ok(app.includes("gadgetCapabilities.render('es', s.gadget_capabilities)"));
  assert.ok(app.includes("gadgetCapabilities.args('es', s.gadget_capabilities || null)"));
});

test('Moneypenny management renames registrations through Hem', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  const list = app.slice(app.indexOf('  async function loadMoneypennies()'), app.indexOf('  async function pingMoneypenny('));
  const rename = app.slice(app.indexOf('  function showRenameMoneypennyModal('), app.indexOf('  async function toggleMoneypennyEnabled('));
  assert.match(list, /data-action="rename"/);
  assert.match(list, /showRenameMoneypennyModal\(mp\)/);
  assert.match(rename, /apiCall\('rename', 'moneypenny', \['-n', name, '--new-name', newName\]\)/);
  assert.match(rename, /closeWizard\(\);\s*loadMoneypennies\(\)/);
});

test('Moneypenny model refresh requests both agents and reports partial failure', async () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  const list = app.slice(app.indexOf('  async function loadMoneypennies()'), app.indexOf('  async function pingMoneypenny('));
  assert.match(list, /data-action="refresh-models"/);
  assert.match(list, /refreshMoneypennyModels\(mp, btn\)/);

  const calls = [];
  const alerts = [];
  let releaseCopilot;
  const context = vm.createContext({
    apiCall: (verb, noun, args) => {
      calls.push([verb, noun, ...args]);
      if (args.at(-1) === 'copilot') {
        return new Promise(resolve => { releaseCopilot = resolve; });
      }
      return Promise.resolve({ status: 'ok', data: { message: 'Refreshed model cache for personal/opencode: 2 models' } });
    },
    alert: message => alerts.push(message),
  });
  const refresh = app.slice(app.indexOf('  async function refreshMoneypennyModels('), app.indexOf('  function showRenameMoneypennyModal('));
  vm.runInContext(`${refresh}\nglobalThis.refresh = refreshMoneypennyModels;`, context);

  const button = { disabled: false, textContent: 'Refresh Models' };
  const pending = context.refresh('personal', button);
  assert.equal(button.disabled, true);
  assert.equal(button.textContent, 'Refreshing…');
  assert.deepEqual(calls, [
    ['refresh-models', '', '-m', 'personal', '--agent', 'opencode'],
    ['refresh-models', '', '-m', 'personal', '--agent', 'copilot'],
  ]);
  releaseCopilot({ status: 'error', message: 'copilot not installed' });
  await pending;
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, 'Refresh Models');
  assert.equal(alerts[0], 'Refreshed model cache for personal/opencode: 2 models\ncopilot refresh failed: copilot not installed');

  const second = context.refresh('personal', button);
  releaseCopilot({ status: 'ok', data: { message: 'Refreshed model cache for personal/copilot: 3 models' } });
  await second;
  assert.equal(button.disabled, false);
  assert.equal(calls.length, 4);
  assert.equal(alerts[1], 'Refreshed model cache for personal/opencode: 2 models\nRefreshed model cache for personal/copilot: 3 models');
});

test('custom compaction threshold is bounded and hidden for agent mode', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  const helper = fs.readFileSync(path.join(__dirname, '../pkg/web/static/compaction-threshold.js'), 'utf8');
  assert.deepEqual(
    [thresholds.DEFAULT, thresholds.LONG_CONTEXT, thresholds.MIN, thresholds.MAX],
    [150000, 800000, 10000, 900000],
  );
  assert.match(app, /id="wiz-compaction-threshold" type="number" min="\$\{COMPACTION_THRESHOLD_MIN\}" max="\$\{COMPACTION_THRESHOLD_MAX\}".*<span>k tokens<\/span>/);
  assert.match(app, /id="es-compaction-threshold" type="number" min="\$\{COMPACTION_THRESHOLD_MIN\}" max="\$\{COMPACTION_THRESHOLD_MAX\}".*<span>k tokens<\/span>/);
  assert.match(app, /input\.disabled = !custom/);
  assert.match(app, /control\.style\.display = custom \? 'flex' : 'none'/);
  assert.match(app, /--compaction-threshold-tokens/);
  assert.match(app, /Custom compaction threshold/);
  assert.match(app, /thresholdDefaultDerived/);
  assert.match(helper, /thousand tokens/);
});

test('Qew threshold helpers follow context defaults and inclusive validation', () => {
  assert.equal(thresholds.defaultForContext(''), 150000);
  assert.equal(thresholds.defaultForContext('default'), 150000);
  assert.equal(thresholds.defaultForContext('long_context'), 800000);
  assert.equal(thresholds.effective(0, 'long_context'), 800000);
  assert.equal(thresholds.effective(150000, 'long_context'), 150000);
  assert.equal(thresholds.defaultDisplayForContext(''), 150);
  assert.equal(thresholds.defaultDisplayForContext('long_context'), 800);
  assert.equal(thresholds.effectiveDisplay(150000, 'long_context'), 150);
  assert.equal(thresholds.toTokens('150'), 150000);
  assert.equal(thresholds.effectiveDisplay(123456, ''), 123.456);
  assert.equal(thresholds.toTokens('123.456'), 123456);
  for (const value of ['10', '123.456', '900']) assert.equal(thresholds.validateDisplay(value), '');
  assert.notEqual(thresholds.validateDisplay('9'), '');
  assert.notEqual(thresholds.validateDisplay('901'), '');
  assert.notEqual(thresholds.validateDisplay('10.0001'), '');
});

test('all-subagents dialog supports keyboard navigation and Escape dismissal', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  assert.match(app, /const closeRoot = overlay\.querySelector\('\.cmd-palette'\) \|\| overlay\.querySelector\('\.modal'\)/);
  assert.match(app, /function refreshAllSubagents\(\)/);
  assert.match(app, /apiCall\('list', 'subsession', \[sessionID\]\)/);
  assert.match(app, /Loading subagents\.\.\./);
  assert.match(app, /function renderAllSubagents\(error, open\)/);
  assert.match(app, /function handleAllSubagentsKey\(e\)/);
  assert.match(app, /e\.key === 'ArrowDown' \|\| e\.key === 'j'/);
  assert.match(app, /e\.key === 'ArrowUp' \|\| e\.key === 'k'/);
  assert.match(app, /openAllSubagent\(allSubagentsCursor\)/);
  assert.match(app, /if \(handleAllSubagentsKey\(e\)\) return/);
});

test('callbacks show their source using muted styling', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  const html = fs.readFileSync(path.join(__dirname, '../pkg/web/static/index.html'), 'utf8');
  assert.match(app, /turn\.source_name \|\| 'subagent'/);
  assert.match(app, /<strong>\$\{callbackSource\}:<\/strong>/);
  assert.match(html, /\.msg\.callback \{ color: var\(--muted\);/);
  assert.match(html, /\.msg\.callback code, \.msg\.callback pre \{ color: var\(--muted\);/);
});

test('Escape on the session list opens its shortcut reference', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  assert.match(app, /function showDashboardShortcuts\(\)/);
  assert.match(app, /aria-label="Session list shortcuts"/);
  assert.match(app, /<kbd>c<\/kbd> Complete selected session/);
  assert.match(app, /else \{\s*e\.preventDefault\(\);\s*showDashboardShortcuts\(\);/);
});

test('Escape closes the session list shortcut reference', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  assert.match(app, /const closeRoot = overlay\.querySelector\('\.cmd-palette'\) \|\| overlay\.querySelector\('\.modal'\)/);
  assert.match(app, /window\._qewCloseDashboardShortcuts = function\(\) \{ closeWizard\(\); \}/);
});

test('chat connection loss preserves the transcript and gates sending', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  assert.match(app, /let qewConnected = false/);
  assert.match(app, /function setConnectionState\(connected\)/);
  assert.match(app, /Disconnected — retrying/);
  assert.match(app, /send\.disabled = !connected \|\| sendInFlight/);
  assert.match(app, /if \(\(!text && !hasAttachments\) \|\| !currentSession \|\| !qewConnected\) return/);
  assert.match(app, /throw new Error\(histResp\.message \|\| 'Unable to load conversation'\)/);
  assert.match(app, /setConnectionState\(false\);\s*\n\s*\}/);
  assert.equal(app.includes("document.getElementById('chat-messages').innerHTML =\n        `<div class=\"empty-state\">Error:"), false);
});
