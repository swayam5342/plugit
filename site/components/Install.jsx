'use client';

import { useLayoutEffect, useRef, useState } from 'react';

const DEFAULT_SPEC = 'https://petstore3.swagger.io/api/v3/openapi.json';
const MAX_LEN = 2048;
const SKIP = new Set(['www', 'api', 'docs', 'developer', 'developers', 'raw', 'com', 'io', 'org', 'net', 'dev', 'app', 'co']);
// A local spec path: plain characters only, ending in a spec extension.
const PATH_OK = /^[A-Za-z0-9 _.~()+\-/\\:]+\.(json|ya?ml)$/i;

// Checks what was typed. Returns { spec } with a safe, normalised value, or { error }.
export function check(raw) {
  const s = raw.trim();
  if (!s) return { spec: '' };
  if (s.length > MAX_LEN) return { error: 'That is longer than 2,048 characters.' };
  if (/[\u0000-\u001f\u007f]/.test(s)) return { error: 'Remove the line breaks and control characters.' };

  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) {
    let u;
    try {
      u = new URL(s);
    } catch {
      return { error: 'That URL is not valid. Check it starts with https:// and has a host.' };
    }
    if (u.protocol !== 'https:' && u.protocol !== 'http:') return { error: 'Use an http or https URL, or a path to a spec file.' };
    if (!u.hostname || !(u.hostname.includes('.') || u.hostname === 'localhost' || u.hostname.startsWith('['))) {
      return { error: 'That URL has no proper host name.' };
    }
    if (u.username || u.password) {
      return { error: 'Leave the credentials out of the URL: plugit asks for the key separately and keeps it in your keyring.' };
    }
    // The parsed form is percent-encoded; encode quotes too so nothing breaks out of the shell quoting.
    return { spec: u.href.replace(/'/g, '%27') };
  }

  if (/^[a-z][a-z0-9+.-]*:(?![\\/])/i.test(s) && !/^[a-z]:[\\/]/i.test(s)) {
    return { error: 'Use an http or https URL, or a path to a spec file.' };
  }
  if (!/\.(json|ya?ml)$/i.test(s)) return { error: 'A spec file ends in .json, .yaml or .yml (Postman collections are .json).' };
  if (!PATH_OK.test(s)) return { error: 'Use a plain file path: letters, digits, spaces and . _ - / \\ : ~ ( ) +' };
  return { spec: s };
}

// A plugit API name from a spec URL or path: lowercase letters, digits, dashes.
function nameFor(spec) {
  let words = [];
  try {
    const u = new URL(spec);
    words = u.hostname.split('.').filter((w) => !SKIP.has(w));
  } catch {
    const base = spec.split(/[\\/]/).pop() || '';
    words = [base.replace(/\.(json|ya?ml)$/i, '')];
  }
  const name = (words[0] || '').toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 32);
  return name || 'my-api';
}

// Quote for a POSIX shell or PowerShell when the argument has anything special.
// check() guarantees there is no single quote left to escape.
function quote(s) {
  return /^[A-Za-z0-9_./:=?%~+-]+$/.test(s) ? s : `'${s}'`;
}

function commands(spec, name) {
  return `go install github.com/swayam5342/plugit/cmd/plugit@latest\nplugit add ${name} ${quote(spec)}\nplugit install ${name}`;
}

// A sentence with a blank: type the spec, read the commands. Nothing leaves the page.
export default function Install() {
  const [spec, setSpec] = useState('');
  const [error, setError] = useState('');
  const [cmd, setCmd] = useState(() => commands(DEFAULT_SPEC, 'my-api'));
  const [copied, setCopied] = useState(false);
  const field = useRef(null);

  // Grow the blank to fit what was typed, so a long URL stays readable.
  useLayoutEffect(() => {
    const el = field.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${el.scrollHeight}px`;
  }, [spec]);

  const change = (value) => {
    const v = value.replace(/[\r\n]+/g, '');
    setSpec(v);
    setError(v.trim() ? (check(v).error || '') : '');
  };

  const submit = (e) => {
    e.preventDefault();
    const r = check(spec);
    if (r.error) {
      setError(r.error);
      field.current?.focus();
      return;
    }
    setError('');
    setCmd(r.spec ? commands(r.spec, nameFor(r.spec)) : commands(DEFAULT_SPEC, 'my-api'));
    setCopied(false);
  };

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(cmd);
      setCopied(true);
      setTimeout(() => setCopied(false), 1800);
    } catch {
      setCopied(false);
    }
  };

  return (
    <section className="install wrap" id="install" aria-labelledby="install-title">
      <h2 id="install-title" className="sr-only">Install PlugIt</h2>
      <form className={`blank${spec ? ' has-value' : ''}`} autoComplete="off" noValidate onSubmit={submit}>
        <div className="blank-sentence">
          <span aria-hidden="true">Plug</span>
          <textarea
            ref={field}
            className="blank-input"
            id="spec"
            name="spec"
            rows={1}
            maxLength={MAX_LEN}
            placeholder="your spec URL"
            aria-label="Your API's spec: an https URL or a path to a .json or .yaml file"
            aria-invalid={error ? 'true' : undefined}
            aria-describedby="spec-help"
            spellCheck={false}
            value={spec}
            onChange={(e) => change(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') submit(e); }}
          />
          <span aria-hidden="true">in</span>
        </div>
        <p id="spec-help" className={error ? 'blank-error' : 'blank-hint'} aria-live="polite">
          {error || 'An https URL, or a path to a .json or .yaml file.'}
        </p>
        <div className="blank-row">
          <button className="button" type="submit">Write my commands</button>
          <p className="blank-note">Nothing is sent anywhere: this only fills in the commands.</p>
        </div>
      </form>
      <div className="commands">
        <pre><code aria-live="polite">{cmd}</code></pre>
        <div className="commands-row">
          <button className="button" type="button" onClick={copy}>{copied ? 'Copied' : 'Copy the commands'}</button>
          <a className="link link-plain" href="https://github.com/swayam5342/plugit/releases">No Go? Download a release</a>
        </div>
        <p className="small muted">Go 1.26.7 or newer (older versions fetch it for you). Windows, macOS and Linux; one binary, no CGO.</p>
      </div>
    </section>
  );
}
