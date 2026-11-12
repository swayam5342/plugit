// The same card in four fields, a step further along each time.
export default function How() {
  return (
    <section className="how" id="how" aria-labelledby="how-title">
      <div className="how-field">
        <h2 id="how-title" className="how-title">From spec to tool</h2>
        <div className="card" data-ui>
          <div className="card-top"><b>pets</b><span className="tui-dim">new</span></div>
          <div className="tui-dim">petstore.json · OpenAPI 3.0</div>
          <div>19 operations</div>
          <div className="tui-dim">base URL pinned</div>
        </div>
        <p className="how-clause">First, point it at a spec.</p>
      </div>
      <div className="how-field">
        <div className="card" data-ui>
          <div className="card-top"><b>pets</b><span className="pill">read-only</span></div>
          <div>8 tools · ≈656 tokens</div>
          <div className="tui-dim">11 writes not offered</div>
          <div className="tui-dim">3 response fields kept</div>
        </div>
        <p className="how-clause">Then choose what the model gets.</p>
      </div>
      <div className="how-field">
        <div className="card" data-ui>
          <div className="card-top"><b>pets</b><span className="tui-ok">installed</span></div>
          <div>Claude Desktop</div>
          <div className="tui-dim">entry added, no secrets</div>
          <div className="tui-dim">backup saved first</div>
        </div>
        <p className="how-clause">Then plug it into your client.</p>
      </div>
      <div className="how-field">
        <div className="card" data-ui>
          <div className="card-top"><b>pets</b><span className="tui-ok">● live</span></div>
          <div>findPetsByStatus · 200</div>
          <div className="tui-dim">42 ms · key masked</div>
          <div className="tui-dim">logged locally</div>
        </div>
        <p className="how-clause">Then see every call it makes.</p>
      </div>
    </section>
  );
}
