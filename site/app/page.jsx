import { STAR } from '@/components/glyphs';
import Picker from '@/components/Picker';
import Screens from '@/components/Screens';
import Film from '@/components/Film';
import Safety from '@/components/Safety';
import How from '@/components/How';
import Clients from '@/components/Clients';
import Tokens from '@/components/Tokens';
import Install from '@/components/Install';
import Footer from '@/components/Footer';
import Reveals from '@/components/Reveals';

export default function Home() {
  return (
    <>
      <header className="nav wrap">
        <a className="wordmark" href="#top" aria-label="PlugIt, back to the top">
          <span className="star" aria-hidden="true">{STAR}</span> plugit
        </a>
        <nav aria-label="Sections">
          <a className="link" href="#how">How it works</a>
          <a className="link" href="#safety">Safety</a>
          <a className="link" href="#clients">Clients</a>
          <a className="link" href="https://github.com/swayam5342/plugit">GitHub</a>
        </nav>
        <a className="button button-small" href="#install">Install</a>
      </header>

      <main id="top">
        {/* The picker is the product, working, on a stage cut like a socket. */}
        <section className="hero" aria-labelledby="hero-title">
          <div className="hero-copy wrap">
            <h1 id="hero-title" className="hero-title lines">
              <span className="line"><span data-line style={{ '--i': 0 }}>Plug any API</span></span>
              <span className="line"><span data-line style={{ '--i': 1 }}>into your AI</span></span>
            </h1>
            <p className="hero-lead" data-rise style={{ '--i': 3 }}>
              PlugIt turns an OpenAPI, Swagger or Postman spec into tools for Claude, Codex, Cursor and
              VS Code. You pick the tools; it keeps your keys and your API safe.
            </p>
            <div className="hero-actions" data-rise style={{ '--i': 4 }}>
              <a className="button" href="#install">Install plugit</a>
              <a className="link link-plain" href="#film">Watch it run</a>
            </div>
          </div>
          <div className="stage">
            <Picker />
          </div>
        </section>

        <Screens />
        <Film />
        <Safety />
        <How />
        <Clients />
        <Tokens />
        <Install />
      </main>

      <Footer />
      <Reveals />
    </>
  );
}
