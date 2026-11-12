'use client';

import { STAR } from '@/components/glyphs';
import { useMemo, useState } from 'react';
import { PETSTORE } from '@/data/petstore';

const METHOD_ORDER = ['GET', 'HEAD', 'OPTIONS', 'POST', 'PUT', 'PATCH', 'DELETE'];
const MODES = ['read-only', 'confirm-writes', 'full'];
const BUDGET = 8000; // the picker's large-toolset threshold (lint L010)

// Same rule as policy.Lists: read-only offers only read tools; the other
// modes offer everything (confirm-writes asks first).
const offered = (t, mode) => mode !== 'read-only' || t.safety === 'read-only';

function groups(tools) {
  const by = new Map();
  for (const t of tools) {
    const g = t.tag || '(untagged)';
    if (!by.has(g)) by.set(g, []);
    by.get(g).push(t);
  }
  for (const list of by.values()) {
    list.sort((a, b) => a.path.localeCompare(b.path) || METHOD_ORDER.indexOf(a.method) - METHOD_ORDER.indexOf(b.method));
  }
  return [...by.entries()].sort(([a], [b]) => a.localeCompare(b));
}

function formatTokens(n) {
  return n < 1000 ? `≈${n}` : `≈${(n / 1000).toFixed(1)}k`;
}

// The PlugIt picker with the real Petstore tools (SPEC §2.3): toggle tools
// and the policy mode, and the budget updates exactly as in the terminal.
export default function Picker() {
  const grouped = useMemo(() => groups(PETSTORE.tools), []);
  const [mode, setMode] = useState('read-only');
  const [selected, setSelected] = useState(() => new Set(PETSTORE.tools.filter((t) => t.safety === 'read-only').map((t) => t.key)));
  const [cursor, setCursor] = useState('GET /pet/findByStatus');
  const [dirty, setDirty] = useState(false);

  const toggle = (key) => {
    setSelected((s) => {
      const n = new Set(s);
      n.has(key) ? n.delete(key) : n.add(key);
      return n;
    });
    setCursor(key);
    setDirty(true);
  };
  const toggleGroup = (list) => {
    setSelected((s) => {
      const n = new Set(s);
      const all = list.every((t) => n.has(t.key));
      for (const t of list) all ? n.delete(t.key) : n.add(t.key);
      return n;
    });
    setDirty(true);
  };

  const picked = PETSTORE.tools.filter((t) => selected.has(t.key));
  const live = picked.filter((t) => offered(t, mode));
  const budget = live.reduce((sum, t) => sum + t.tokens, 0);
  const writes = picked.filter((t) => t.safety !== 'read-only').length;
  const notOffered = picked.length - live.length;
  const current = PETSTORE.tools.find((t) => t.key === cursor) || PETSTORE.tools[0];
  const gaugeColor = budget > BUDGET ? 'var(--t-err)' : budget * 3 > BUDGET * 2 ? 'var(--t-warn)' : 'var(--t-ok)';

  let stats = `${picked.length} ${picked.length === 1 ? 'tool' : 'tools'} · ${formatTokens(budget)} tokens · ${writes} write`;
  if (notOffered > 0) stats += ` · ${notOffered} not offered`;

  return (
    <div className="tui picker" data-ui data-mode={mode} aria-label="PlugIt's tool picker with the Petstore API. Toggle tools and the policy mode.">
      <div className="tui-box">
        <div className="tui-head">
          <span>
            <span className="tui-accent">{STAR}</span> <b>plugit</b> <span className="tui-dim">·</span> <b>pets</b>{' '}
            {dirty && <span className="tui-warn">● unsaved</span>}
          </span>
          <button
            className="pill"
            type="button"
            aria-label={`Policy mode: ${mode}. Press to change.`}
            onClick={() => setMode((m) => MODES[(MODES.indexOf(m) + 1) % MODES.length])}
          >
            {mode}
          </button>
        </div>
        <div className="tui-dim">Pick the tools to expose · {PETSTORE.tools.length} operations</div>
      </div>

      <div className="tui-list" role="group" aria-label="Petstore operations">
        {grouped.map(([name, list]) => (
          <div key={name} role="presentation">
            <button type="button" className="g-row" onClick={() => toggleGroup(list)} aria-label={`Toggle every tool in ${name}`}>
              <span className="tui-dim">▾</span> <b className="tui-accent">{name}</b>{' '}
              <span className="count tui-ok">{list.filter((t) => selected.has(t.key)).length}/{list.length}</span>
            </button>
            {list.map((t) => {
              const on = selected.has(t.key);
              const isOffered = offered(t, mode);
              let extra = isOffered ? (on ? formatTokens(t.tokens) : '') : 'not offered';
              if (isOffered && mode === 'confirm-writes' && t.safety !== 'read-only') extra = on ? `${formatTokens(t.tokens)} confirm` : 'confirm';
              return (
                <button
                  key={t.key}
                  type="button"
                  role="checkbox"
                  aria-checked={on}
                  className={`t-row${isOffered ? '' : ' off'}${cursor === t.key ? ' is-cursor' : ''}`}
                  onClick={() => toggle(t.key)}
                  onFocus={() => setCursor(t.key)}
                  onMouseEnter={() => setCursor(t.key)}
                >
                  <span className="dot">{on ? '●' : '○'}</span>
                  <span className={`m ${t.method.toLowerCase()}`}>{t.method}</span>
                  <span className="p">{t.path}</span>
                  <span className="n">{t.name}</span>
                  <span className="x">{extra}</span>
                </button>
              );
            })}
          </div>
        ))}
      </div>

      <div className="tui-detail">
        <span className="tui-dim">⎿</span>{'  '}
        <span className={`m ${current.method.toLowerCase()}`}>{current.method}</span> {current.path}{' '}
        <span className="tui-dim">{current.desc}</span>
      </div>
      <div className="tui-box tui-prompt">
        <span className="tui-dim">&gt; / filter · space toggle · m mode · i details · s save</span>
      </div>
      <div className="tui-foot">
        <span className="stats">{stats}</span>
        <span className="gauge" aria-hidden="true">
          <i style={{ width: `${Math.max(Math.min((budget / BUDGET) * 100, 100), budget ? 1 : 0)}%`, background: gaugeColor }} />
        </span>
        <span className="tui-dim shortcuts">? for shortcuts</span>
      </div>
    </div>
  );
}
