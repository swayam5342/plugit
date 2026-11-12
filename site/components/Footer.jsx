'use client';

import { useEffect, useState } from 'react';

const REPO = 'https://github.com/swayam5342/plugit';

const LINKS = [
  ['Demo', '#film'],
  ['Install', '#install'],
  ['README', `${REPO}#readme`],
  ['GitHub', REPO],
  ['Releases', `${REPO}/releases`],
  ['Security policy', `${REPO}/blob/main/SECURITY.md`],
  ['Report an issue', `${REPO}/issues`],
  ['Apache-2.0 licence', `${REPO}/blob/main/LICENSE`],
];

function now() {
  return new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' }).format(new Date());
}

// A paper band to close the page: the call on the left, the links on the right.
export default function Footer() {
  const [clock, setClock] = useState('');
  useEffect(() => {
    setClock(now());
    const id = setInterval(() => setClock(now()), 30_000);
    return () => clearInterval(id);
  }, []);

  return (
    <footer className="footer" aria-label="About PlugIt">
      <div className="footer-top">
        <div className="footer-call">
          <h2>Your API.<br />Your keys.</h2>
          <a className="button footer-button" href={REPO}>Get the code on GitHub</a>
        </div>
        <nav className="footer-links" aria-label="Project links">
          {LINKS.map(([label, href]) => <a key={label} href={href}>{label}</a>)}
        </nav>
      </div>
      <p className="footer-credit">
        Local only: no accounts, no hosted backend, no LLM calls, no telemetry.
        {clock && <> It&apos;s <span suppressHydrationWarning>{clock}</span> where you are, and that&apos;s where your keys stay.</>}
        {' '}Demo recorded with vhs. Instrument Sans by Instrument, OFL.
      </p>
    </footer>
  );
}
