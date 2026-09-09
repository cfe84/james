(function(root) {
  'use strict';

  const permissions = [
    { name: 'memory', label: 'Session memory', defaultValue: true },
    { name: 'subagents', label: 'Create and communicate with own subagents; reply to parent', defaultValue: true },
    { name: 'agents', label: 'Discover and message any agents (no management access)', defaultValue: false },
    { name: 'scheduling', label: 'Schedule prompts for this session', defaultValue: true },
  ];

  function values(capabilities) {
    return Object.fromEntries(permissions.map(p => [
      p.name, typeof capabilities?.[p.name] === 'boolean' ? capabilities[p.name] : p.defaultValue,
    ]));
  }

  function render(prefix, capabilities) {
    const current = values(capabilities);
    return '<label>Gadget permissions</label>' + permissions.map(p => `
      <div class="toggle-row">
        <input type="checkbox" id="${prefix}-gadget-${p.name}"${current[p.name] ? ' checked' : ''}>
        <label for="${prefix}-gadget-${p.name}" style="margin:0;color:var(--text)">${p.label}</label>
      </div>`).join('') +
      '<div style="font-size:0.8em;color:var(--muted);margin-bottom:8px">Operator settings for agent tools. Notifications to you are always available.</div>';
  }

  function args(prefix, original, documentRoot = document) {
    const previous = original === undefined ? null : values(original);
    return permissions.flatMap(p => {
      const enabled = documentRoot.getElementById(`${prefix}-gadget-${p.name}`).checked;
      return previous && previous[p.name] === enabled ? [] : [`--gadget-${p.name}=${enabled}`];
    });
  }

  const controls = { values, render, args };
  if (typeof module !== 'undefined' && module.exports) module.exports = controls;
  else root.gadgetCapabilities = controls;
})(typeof globalThis !== 'undefined' ? globalThis : this);
