const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const controls = require('../pkg/web/static/gadget-capabilities.js');

const defaults = { memory: true, subagents: true, agents: false, scheduling: true };
const allOff = { memory: false, subagents: false, agents: false, scheduling: false };
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

test('create and copy emit all four explicit permissions, including false', () => {
  assert.deepEqual(controls.args('wiz', undefined, documentFor(allOff)), [
    '--gadget-memory=false', '--gadget-subagents=false',
    '--gadget-agents=false', '--gadget-scheduling=false',
  ]);
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

test('reusable controls render accessible toggles and permanent notifications', () => {
  const rendered = controls.render('wiz', defaults);
  for (const name of Object.keys(defaults)) {
    assert.ok(rendered.includes(`for="wiz-gadget-${name}"`));
    assert.ok(rendered.includes(`id="wiz-gadget-${name}"${defaults[name] ? ' checked' : ''}>`));
  }
  assert.equal((rendered.match(/type="checkbox"/g) || []).length, 4);
  assert.match(rendered, /Notifications to you are always available/);
  assert.match(rendered, /no management access/);
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
