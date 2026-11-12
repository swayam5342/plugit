'use client';

import { STAR, CHECK, CROSS, PROMPT } from '@/components/glyphs';
import { useEffect, useRef, useState } from 'react';

// Real PlugIt screens as true-size fragments. The centred one is lit (the
// product's light theme); the row scrolls, snaps and can be dragged.
const PANELS = [
  {
    name: 'Tool detail',
    text: 'Parameters with their locations, the token cost, lint findings and the exact input schema, before anything reaches a model.',
    body: (
      <>
        <div className="tui-box">
          <div className="tui-head"><span><span className="tui-accent">{STAR}</span> <b>plugit</b> <span className="tui-dim">· tool ·</span> <b>findPetsByStatus</b></span><span className="pill">read-only</span></div>
          <div><span className="m get">GET</span> /pet/findByStatus</div>
        </div>
        <div className="tui-sec">Parameters</div>
        <div>  <b>status</b>  <span className="tui-dim">query · string · required</span></div>
        <div className="tui-sec">Token cost</div>
        <div>  ≈103 tokens <span className="tui-dim">in the tool list</span></div>
        <div className="tui-sec">Lint</div>
        <div>  <span className="tui-ok">{CHECK} no findings</span></div>
        <div className="tui-sec">Input schema</div>
        <pre className="tui-pre">{`  {
    "properties": {
      "status": {
        "default": "available",
        "enum": ["available", "pending", "sold"],
        "type": "string"
      }
    },
    "required": ["status"]
  }`}</pre>
      </>
    ),
  },
  {
    name: 'Lint',
    text: 'Thirteen rules, from missing descriptions to instructions hidden in a spec. Errors exit 1, so CI can stop a bad spec.',
    body: (
      <>
        <div><span className="tui-accent">{PROMPT}</span> plugit lint notes</div>
        <div><span className="tui-err">{CROSS} error L007</span>  [default] listNotes (GET /notes):</div>
        <div>    summary reads like an instruction to the</div>
        <div>    model: <span className="tui-warn">&quot;Ignore previous instructions&quot;</span></div>
        <div><span className="tui-err">{CROSS} error L007</span>  [default] listNotes (GET /notes):</div>
        <div>    hidden text: summary contained invisible</div>
        <div>    or bidi characters (removed)</div>
        <div><span className="tui-warn">! warn  L007</span>  [default] getNote (GET /notes/{'{id}'}):</div>
        <div>    description embeds a URL (evil.example.test)</div>
        <div><span className="tui-dim">· info  L008</span>  [default] oldSearch (GET /search):</div>
        <div>    the spec marks this operation deprecated</div>
        <div> </div>
        <div>notes: 2 errors, 1 warning, 1 info</div>
        <div><span className="tui-err">plugit: lint found 2 errors</span></div>
      </>
    ),
  },
  {
    name: 'Try',
    text: 'One real call from the terminal. The key is masked in the request and in anything the API echoes back.',
    body: (
      <>
        <div><span className="tui-accent">{PROMPT}</span> plugit try pets findPetsByStatus</div>
        <div><span className="tui-dim">→</span> GET https://petstore3.swagger.io/api/v3/</div>
        <div>    pet/findByStatus?status=available</div>
        <div>  X-Api-Key: <span className="tui-accent">***</span></div>
        <div><span className="tui-dim">←</span></div>
        <div><span className="tui-ok">HTTP 200 OK</span></div>
        <div>Filtered to fields: id, name, status</div>
        <pre className="tui-pre">{`[
  { "id": 1, "name": "Rex", "status": "available" },
  { "id": 2, "name": "Mittens", "status": "available" }
]`}</pre>
      </>
    ),
  },
  {
    name: 'Logs',
    text: 'A live view of every call your AI makes: tool, outcome, status, latency. Enter opens the redacted arguments.',
    body: (
      <>
        <div className="tui-box"><div className="tui-head"><span><span className="tui-accent">{STAR}</span> <b>plugit</b> <span className="tui-dim">· logs ·</span> <b>pets</b></span><span className="tui-ok">● live</span></div></div>
        <div className="tui-cols"><span className="tui-dim">18:02:11</span><b>findPetsByStatus</b><span className="tui-ok">ok 200</span><span className="tui-dim">42ms</span></div>
        <div className="tui-cols"><span className="tui-dim">18:02:19</span><b>getPetById</b><span className="tui-ok">ok 200</span><span className="tui-dim">38ms</span></div>
        <div className="tui-cols"><span className="tui-dim">18:02:31</span><b>getPetById</b><span className="tui-err">http_error 404</span><span className="tui-dim">35ms</span></div>
        <div className="tui-cols"><span className="tui-dim">18:03:02</span><b>addPet</b><span className="tui-warn">denied</span><span className="tui-dim">0.2ms</span></div>
        <div className="tui-cols"><span className="tui-accent">›</span><b>getInventory</b><span className="tui-ok">ok 200</span><span className="tui-dim">51ms</span></div>
        <div className="tui-box"><div><span className="tui-dim">args</span>     {'{"status":"available"}'}</div><div><span className="tui-dim">error</span>    <span className="tui-dim">(none)</span></div></div>
        <div className="tui-dim">5 calls · 2 errors   ↑↓ move · enter details</div>
      </>
    ),
  },
  {
    name: 'Stats',
    text: "Calls, error rate and p50 and p95 latency per tool, and what each API's toolset costs in tokens.",
    body: (
      <>
        <div className="tui-box"><div className="tui-head"><span><span className="tui-accent">{STAR}</span> <b>plugit</b> <span className="tui-dim">· stats ·</span> <b>all APIs</b></span><span className="tui-accent">last 7 days</span></div><div className="tui-dim">41 calls · 3 errors · sorted by calls</div></div>
        <div><span className="tui-accent"><b>pets</b></span>  <span className="tui-dim">41 calls · 7% errors · ≈656 tokens · 8 tools</span></div>
        <div className="tui-cols tui-dim"><span>tool</span><span /><span>calls · p50</span><span>p95</span></div>
        <div className="tui-cols"><span /><b>findPetsByStatus</b><span>22 · 40ms</span><span>88ms</span></div>
        <div className="tui-cols"><span /><b>getPetById</b><span>12 · 36ms</span><span>61ms</span></div>
        <div className="tui-cols"><span /><b>getInventory</b><span>5 · 49ms</span><span>70ms</span></div>
        <div className="tui-cols"><span /><b>addPet</b><span>2 · 0.2ms</span><span>0.3ms</span></div>
      </>
    ),
  },
  {
    name: 'Sync',
    text: 'When the spec changes: what was added, changed or dropped. Every override stays, and the base URL never moves.',
    body: (
      <>
        <div className="tui-box"><div className="tui-head"><span><span className="tui-accent">{STAR}</span> <b>plugit</b> <span className="tui-dim">· sync ·</span> <b>pets</b></span><span className="tui-dim">+1 ~1 -1</span></div></div>
        <div><span className="tui-warn">! The spec&apos;s servers changed:</span></div>
        <div>  petstore3.swagger.io → new-home.example.test</div>
        <div className="tui-dim">  base_url stays https://petstore3.swagger.io/api/v3</div>
        <div className="tui-sec">Added (1)</div>
        <div>  <span className="tui-accent">›</span> <span className="tui-accent">●</span> <span className="m get">GET</span> /pet/{'{petId}'}/visits  <b>listVisits</b></div>
        <div className="tui-sec">Changed (1)</div>
        <div>  GET /pet/findByStatus  <span className="tui-dim">description</span>  <span className="tui-warn">served in default</span></div>
        <div className="tui-sec">Removed (1)</div>
        <div>  POST /pet/{'{petId}'}/uploadImage <span className="tui-dim">overrides kept</span></div>
      </>
    ),
  },
  {
    name: 'Install',
    text: 'Pick the clients you use. You see the diff of each config, and a backup is made before anything is written.',
    body: (
      <>
        <div className="tui-box"><div><span className="tui-accent">{STAR}</span> <b>plugit install</b></div><div className="tui-dim">Choose the MCP clients (space to tick, enter to confirm)</div></div>
        <div>  <span className="tui-accent">›</span> <span className="tui-accent">●</span> <b>Claude Desktop</b>   <span className="tui-ok">detected</span></div>
        <div>    <span className="tui-accent">●</span> Claude Code      <span className="tui-ok">detected</span></div>
        <div>    <span className="tui-dim">○</span> OpenAI Codex</div>
        <div>    <span className="tui-accent">●</span> Cursor           <span className="tui-ok">detected</span></div>
        <div>    <span className="tui-dim">○</span> VS Code (GitHub Copilot)</div>
        <div>    <span className="tui-dim">○</span> Windsurf</div>
        <div>    <span className="tui-dim">○</span> Gemini CLI</div>
        <div>    <span className="tui-dim">○</span> LM Studio</div>
        <div className="tui-dim">  3 selected   space toggle · enter confirm</div>
      </>
    ),
  },
];

const AUTOPLAY_MS = 4000;
const IDLE_AFTER_TOUCH_MS = 8000;

export default function Screens() {
  const row = useRef(null);
  const [lit, setLit] = useState(0);
  const [playing, setPlaying] = useState(false);
  const litRef = useRef(0);
  const hold = useRef({ hover: false, focus: false, touchedAt: 0 });

  // Scroll the row itself (never the page) so panel i sits in the middle.
  const show = (i) => {
    const el = row.current;
    const child = el?.children[i];
    if (!child) return;
    const er = el.getBoundingClientRect();
    const cr = child.getBoundingClientRect();
    el.scrollTo({ left: el.scrollLeft + (cr.left - er.left) - (el.clientWidth - cr.width) / 2, behavior: 'smooth' });
  };
  const go = (step) => show((litRef.current + step + PANELS.length) % PANELS.length);
  const touched = () => { hold.current.touchedAt = Date.now(); };

  // The lit panel is the one nearest the row's centre.
  useEffect(() => {
    const el = row.current;
    if (!el) return undefined;
    let frame = 0;
    const update = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const mid = el.getBoundingClientRect().left + el.clientWidth / 2;
        let best = 0;
        let bestDist = Infinity;
        [...el.children].forEach((p, i) => {
          const r = p.getBoundingClientRect();
          const d = Math.abs(r.left + r.width / 2 - mid);
          if (d < bestDist) { bestDist = d; best = i; }
        });
        litRef.current = best;
        setLit(best);
      });
    };
    update();
    el.addEventListener('scroll', update, { passive: true });
    window.addEventListener('resize', update);

    // Drag to scroll with a mouse (touch scrolls natively).
    let down = false, startX = 0, startLeft = 0;
    const onDown = (e) => {
      touched();
      if (e.pointerType !== 'mouse') return;
      down = true; startX = e.clientX; startLeft = el.scrollLeft; el.style.scrollSnapType = 'none';
    };
    const onMove = (e) => { if (down) el.scrollLeft = startLeft - (e.clientX - startX); };
    const onUp = () => { if (!down) return; down = false; el.style.scrollSnapType = ''; touched(); };
    el.addEventListener('pointerdown', onDown);
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onUp);
    return () => {
      el.removeEventListener('scroll', update);
      window.removeEventListener('resize', update);
      el.removeEventListener('pointerdown', onDown);
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onUp);
    };
  }, []);

  // Slide on its own while in view, unless the visitor prefers less motion,
  // is pointing at it, has focus in it, or touched it in the last few seconds.
  useEffect(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return undefined;
    setPlaying(true);
    return undefined;
  }, []);
  useEffect(() => {
    if (!playing) return undefined;
    const el = row.current;
    let inView = false;
    const io = new IntersectionObserver(([e]) => { inView = e.isIntersecting; }, { threshold: 0.5 });
    io.observe(el);
    const id = setInterval(() => {
      const h = hold.current;
      if (!inView || document.hidden || h.hover || h.focus || Date.now() - h.touchedAt < IDLE_AFTER_TOUCH_MS) return;
      go(1);
    }, AUTOPLAY_MS);
    return () => { clearInterval(id); io.disconnect(); };
  }, [playing]);

  return (
    <section className="screens" id="screens" aria-labelledby="screens-title">
      <div className="wrap screens-head">
        <h2 id="screens-title" className="head" data-reveal>Every screen, in your terminal</h2>
      </div>
      <div className="screens-body">
        <div className="screens-row" ref={row} tabIndex={0} aria-label="PlugIt screens, scroll sideways"
          onPointerEnter={(e) => { if (e.pointerType === 'mouse') hold.current.hover = true; }}
          onPointerLeave={() => { hold.current.hover = false; }}
          onFocus={() => { hold.current.focus = true; }}
          onBlur={() => { hold.current.focus = false; }}
          onKeyDown={(e) => { if (e.key === 'ArrowRight') { e.preventDefault(); touched(); go(1); } if (e.key === 'ArrowLeft') { e.preventDefault(); touched(); go(-1); } }}>
          {PANELS.map((p, i) => (
            <article key={p.name} className={`panel${i === lit ? ' is-lit' : ''}`} aria-label={p.name}>
              <div className="tui" data-ui>{p.body}</div>
            </article>
          ))}
        </div>
        <div className="screens-caption" aria-live={playing ? 'off' : 'polite'}>
          <h3 className="screens-name">{PANELS[lit].name}</h3>
          <p>{PANELS[lit].text}</p>
          <p className="screens-hint small">Screen {lit + 1} of {PANELS.length}. {playing ? 'It moves on by itself; drag or use the arrows to take over.' : 'Drag the row, or use the arrows.'}</p>
          <div className="screens-arrows">
            <button className="arrow" type="button" onClick={() => { touched(); go(-1); }} aria-label="Previous screen">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 5l-7 7 7 7" /></svg>
            </button>
            <button className="arrow" type="button" onClick={() => { touched(); go(1); }} aria-label="Next screen">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 5l7 7-7 7" /></svg>
            </button>
            <button className="arrow" type="button" onClick={() => setPlaying((v) => !v)} aria-label={playing ? 'Pause the slides' : 'Play the slides'}>
              {playing
                ? <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 5v14M15 5v14" /></svg>
                : <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5l11 7-11 7z" /></svg>}
            </button>
          </div>
        </div>
      </div>
    </section>
  );
}
