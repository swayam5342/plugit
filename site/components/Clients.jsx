// Two-ended rows in one column: what PlugIt plugs into, reads and signs in with.
const GROUPS = [
  {
    word: 'Clients',
    rows: [
      ['#d97757', 'Claude Desktop', 'its config file'], // taste-skill-ignore: Claude's own mark colour, as this row's identity dot
      ['#e8875b', 'Claude Code', 'claude mcp add'],
      ['#ffffff', 'OpenAI Codex', 'codex mcp add'],
      ['#9aa39a', 'Cursor', '~/.cursor/mcp.json'],
      ['#4aa3f0', 'VS Code with GitHub Copilot', 'user mcp.json'],
      ['#3ecfb2', 'Windsurf', 'mcp_config.json'],
      ['#7aa7ff', 'Gemini CLI', '~/.gemini/settings.json'],
      ['#c6f19d', 'LM Studio', '~/.lmstudio/mcp.json'],
      ['ring', 'Any other MCP client', 'stdio or HTTP'],
    ],
  },
  {
    word: 'Specs',
    rows: [
      ['#8cc98f', 'OpenAPI 3.0 and 3.1', 'JSON or YAML'],
      ['#8cc98f', 'Swagger 2.0', 'JSON or YAML'],
      ['#8cc98f', 'Postman collections', 'v2.0, v2.1'],
      ['#8cc98f', 'Form and multipart bodies', '611 of 612 Stripe ops'],
    ],
  },
  {
    word: 'Credentials',
    rows: [
      ['#e5c07b', 'Bearer, header or query keys', 'keyring or env'],
      ['#e5c07b', 'Basic auth', 'user:password'],
      ['#e5c07b', 'OAuth2 client credentials', 'your token_url'],
      ['#e5c07b', 'OAuth2 device sign-in', 'refresh in keyring'],
    ],
  },
];

function Group({ g }) {
  return (
    <div>
      <p className="group-word" data-reveal>{g.word}</p>
      <ul className="rows">
        {g.rows.map(([c, name, val]) => (
          <li key={name}>
            <span
              className="dot"
              style={c === 'ring' ? { '--c': 'var(--surface)', boxShadow: 'inset 0 0 0 2px #9aa39a' } : { '--c': c }}
            />
            <span>{name}</span>
            <span className="val">{val}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

// The heading, then two columns: the clients, and the specs and credentials.
export default function Clients() {
  return (
    <section className="clients wrap" id="clients" aria-labelledby="clients-title">
      <h2 id="clients-title" className="clients-title" data-reveal>Works where you work</h2>
      <div className="clients-colgroup"><Group g={GROUPS[0]} /></div>
      <div className="clients-colgroup"><Group g={GROUPS[1]} /><Group g={GROUPS[2]} /></div>
    </section>
  );
}
