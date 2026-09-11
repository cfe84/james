const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const controls = require('../pkg/web/static/gadget-capabilities.js');

const defaults = { memory: true, subagents: true, agents: false, create_agents: false, edit_sessions: false, edit_own_session: false, edit_own_subagents: false, traits: false, scheduling: true };
const allOff = { memory: false, subagents: false, agents: false, create_agents: false, edit_sessions: false, edit_own_session: false, edit_own_subagents: false, traits: false, scheduling: false };
const documentFor = (values) => ({
  getElementById(id) {
    return { checked: values[id.split('-').at(-1)] };
  },
});

test('legacy details use defaults without replacing explicit false', () => {
  assert.deepEqual(controls.values(), defaults);
  assert.deepEqual(controls.values(null), defaults);
  assert.deepEqual(controls.values(allOff), allOff);
  assert.deepEqual(controls.values({ agents: true }), { ...defaults, agents: true });
});

test('create and copy emit all six explicit permissions, including false', () => {
  assert.deepEqual(controls.args('wiz', undefined, documentFor(allOff)), [
    '--gadget-memory=false', '--gadget-subagents=false',
    '--gadget-agents=false', '--gadget-create-agents=false', '--gadget-edit-sessions=false', '--gadget-edit-own-session=false', '--gadget-edit-own-subagents=false',
    '--gadget-traits=false', '--gadget-scheduling=false',
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

test('reusable controls render accessible toggles and permanent notifications', () => {
  const rendered = controls.render('wiz', defaults);
  for (const name of Object.keys(defaults)) {
    assert.ok(rendered.includes(`for="wiz-gadget-${name}"`));
    assert.ok(rendered.includes(`id="wiz-gadget-${name}"${defaults[name] ? ' checked' : ''}>`));
  }
  assert.equal((rendered.match(/type="checkbox"/g) || []).length, 9);
  assert.match(rendered, /Notifications to you are always available/);
  assert.match(rendered, /no management access/);
  assert.match(rendered, /shared trait bodies.*future use by all agents/);
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

test('all-subagents dialog supports keyboard navigation and Escape dismissal', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  assert.match(app, /const closeRoot = modal \|\| overlay\.querySelector\('\.cmd-palette'\)/);
  assert.match(app, /function handleAllSubagentsKey\(e\)/);
  assert.match(app, /e\.key === 'ArrowDown' \|\| e\.key === 'j'/);
  assert.match(app, /e\.key === 'ArrowUp' \|\| e\.key === 'k'/);
  assert.match(app, /openAllSubagent\(allSubagentsCursor\)/);
  assert.match(app, /if \(handleAllSubagentsKey\(e\)\) return/);
});

test('Escape on the session list opens its shortcut reference', () => {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/web/static/app.js'), 'utf8');
  assert.match(app, /function showDashboardShortcuts\(\)/);
  assert.match(app, /aria-label="Session list shortcuts"/);
  assert.match(app, /<kbd>c<\/kbd> Complete selected session/);
  assert.match(app, /else \{\s*e\.preventDefault\(\);\s*showDashboardShortcuts\(\);/);
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
