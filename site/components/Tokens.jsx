import { PETSTORE } from '@/data/petstore';

const MAX = Math.max(...PETSTORE.tools.map((t) => t.tokens));
const byCost = (a, b) => b.tokens - a.tokens;
const READS = PETSTORE.tools.filter((t) => t.safety === 'read-only').sort(byCost);
const WRITES = PETSTORE.tools.filter((t) => t.safety !== 'read-only').sort(byCost);
const sum = (list) => list.reduce((s, t) => s + t.tokens, 0);
const READ_TOKENS = sum(READS);
const WRITE_TOKENS = sum(WRITES);
const SAVED = Math.round((1 - READ_TOKENS / PETSTORE.total) * 100);

function Group({ title, note, list, total, offered, start }) {
  return (
    <div className={`tok-group${offered ? ' is-offered' : ''}`}>
      <div className="tok-head">
        <span className="tok-title">{title}</span>
        <span className="tok-total">≈{total.toLocaleString('en')} tokens · {note}</span>
      </div>
      <ul className="tok-rows">
        {list.map((t, i) => (
          <li key={t.key} className="tok-row" style={{ '--w': `${((t.tokens / MAX) * 100).toFixed(1)}%`, '--n': start + i }}>
            <span className="tok-name">{t.name}</span>
            <span className="tok-track" aria-hidden="true"><i /></span>
            <span className="tok-val">≈{t.tokens}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

// PlugIt's own token estimate for every Petstore tool, split the way
// read-only mode splits them: what the model reads, and what it never sees.
export default function Tokens() {
  return (
    <section className="tokens" id="tokens" aria-labelledby="tokens-title">
      <div className="tokens-inner wrap">
        <div className="tokens-copy">
          <h2 id="tokens-title" className="tokens-title" data-reveal>
            <span className="figure">{SAVED}%</span>
            <span className="claim">fewer tokens, the same reads</span>
          </h2>
          <p data-reveal>
            Offer all nineteen Petstore tools and the model reads ≈{PETSTORE.total.toLocaleString('en')} tokens of
            tool list before it starts. In read-only mode it gets the eight read tools, ≈{READ_TOKENS}, and the
            writes never reach it. Each bar is PlugIt&apos;s own estimate for one tool.
          </p>
        </div>
        <div className="tok-chart" data-ui aria-label="Estimated tokens per Petstore tool">
          <Group title="Offered in read-only" note={`${READS.length} read tools`} list={READS} total={READ_TOKENS} offered start={0} />
          <Group title="Not offered" note={`${WRITES.length} writes`} list={WRITES} total={WRITE_TOKENS} start={READS.length} />
        </div>
      </div>
    </section>
  );
}
