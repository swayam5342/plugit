'use client';

import { useEffect, useRef, useState } from 'react';
import { asset } from '@/lib';

// The real vhs recording, with its moments as chapters that seek the video.
const CHAPTERS = [
  { at: 2.6, still: 'still-picker.jpg', label: 'Pick the tools', alt: "The picker with Petstore's tools grouped by tag" },
  { at: 4.5, still: 'still-detail.jpg', label: 'Inspect one', alt: 'The tool detail for findPetsByStatus' },
  { at: 10.5, still: 'still-try.jpg', label: 'Call it', alt: 'A real call with the response filtered to three fields' },
  { at: 14.5, still: 'still-chooser.jpg', label: 'Choose a client', alt: 'The client chooser listing eight MCP clients' },
  { at: 18.6, still: 'still-install.jpg', label: 'Installed', alt: 'The install diff, with a backup made first' },
];

export default function Film() {
  const video = useRef(null);
  const [current, setCurrent] = useState(-1);

  useEffect(() => {
    const v = video.current;
    if (!v) return;
    const onTime = () => {
      let idx = -1;
      CHAPTERS.forEach((c, i) => { if (v.currentTime >= c.at - 0.3) idx = i; });
      setCurrent(idx);
    };
    v.addEventListener('timeupdate', onTime);
    // Respect reduced motion: no autoplay, the poster stays until asked.
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) v.pause();
    return () => v.removeEventListener('timeupdate', onTime);
  }, []);

  const seek = (at) => {
    const v = video.current;
    if (!v) return;
    v.currentTime = at;
    v.play().catch(() => {});
  };

  return (
    <section className="film wrap" id="film" aria-labelledby="film-title">
      <div className="film-head">
        <h2 id="film-title" className="head" data-reveal>Twenty seconds, start to finish</h2>
        <p data-reveal>
          Adding the Petstore API, inspecting a tool, keeping three response fields, linting, one real call, then
          installing into Claude Desktop. Recorded in a terminal, not mocked up.
        </p>
      </div>
      <figure className="film-frame">
        <video
          ref={video}
          className="film-video"
          src={asset('/media/demo.mp4')}
          poster={asset('/media/demo-poster.jpg')}
          width={1280}
          height={760}
          muted
          loop
          playsInline
          autoPlay
          preload="metadata"
          aria-label="Screen recording of PlugIt in a terminal"
        />
      </figure>
      <ol className="chapters" aria-label="Jump to a moment">
        {CHAPTERS.map((c, i) => (
          <li key={c.at}>
            <button type="button" onClick={() => seek(c.at)} aria-current={current === i ? 'true' : undefined}>
              <img src={asset(`/media/${c.still}`)} width={1280} height={760} alt={c.alt} loading="lazy" />
              <span>{c.label}</span>
            </button>
          </li>
        ))}
      </ol>
    </section>
  );
}
