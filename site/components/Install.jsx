'use client';

import { useState } from 'react';

const DEFAULT_SPEC = 'https://petstore3.swagger.io/api/v3/openapi.json';
const SKIP = new Set(['www', 'api', 'docs', 'developer', 'developers', 'raw', 'com', 'io', 'org', 'net', 'dev', 'app', 'co']);

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
function quote(s) {
  return /^[A-Za-z0-9_./:=?%~+-]+$/.test(s) ? s : `'${s.replace(/'/g, "''")}'`;
}

function commands(spec, name) {
  return `go install github.com/swayam5342/plugit/cmd/plugit@latest\nplugit add ${name} ${quote(spec)}\nplugit install ${name}`;
}

// A sentence with a blank: type the spec, read the commands. Nothing leaves the page.
export default function Install() {
  const [spec, setSpec] = useState('');
  const [cmd, setCmd] = useState(() => commands(DEFAULT_SPEC, 'my-api'));
  const [copied, setCopied] = useState(false);

  const submit = (e) => {
    e.preventDefault();
    const s = spec.trim();
    setCmd(s ? commands(s, nameFor(s)) : commands(DEFAULT_SPEC, 'my-api'));
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
      <form className="blank" autoComplete="off" onSubmit={submit}>
        <h2 id="install-title" className="blank-sentence">
          <span>Plug</span>
          <label className="sr-only" htmlFor="spec">Your API&apos;s spec URL or file</label>
          <input className="blank-input" id="spec" name="spec" type="text" placeholder="your spec URL" spellCheck={false}
            value={spec} onChange={(e) => setSpec(e.target.value)} />
          <span>in</span>
        </h2>
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
