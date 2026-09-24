// promptfoo provider that runs a case through `claude -p` instead of the
// Messages API, so the suite (and the phase-7 effort sweep) can run on a Claude
// subscription when no ANTHROPIC_API_KEY is funded.
//
// Isolation mirrors a bare API call as closely as the CLI allows: the agent
// markdown REPLACES the Claude Code system prompt (--system-prompt), no tools,
// no settings/CLAUDE.md/hooks (--setting-sources ""), no MCP, no skills, no
// session written, and the process runs in the OS temp dir so no project
// context leaks in. What differs from the API: the CLI still adds its own
// small environment preamble, and thinking is always adaptive.
//
// config: { model: "<id>", effort?: "low|medium|high|xhigh|max", timeoutMs? }
const { spawn } = require('node:child_process');
const os = require('node:os');

class ClaudeCliProvider {
  constructor(options = {}) {
    this.config = options.config || {};
    this.providerId = options.id || `claude-cli:${this.config.model}:${this.config.effort || 'default'}`;
  }

  id() {
    return this.providerId;
  }

  async callApi(prompt) {
    let system = '';
    let user = prompt;
    try {
      const msgs = JSON.parse(prompt);
      if (Array.isArray(msgs)) {
        system = msgs.filter((m) => m.role === 'system').map((m) => m.content).join('\n\n');
        user = msgs.filter((m) => m.role !== 'system').map((m) => m.content).join('\n\n');
      }
    } catch {
      // A plain-text prompt (the llm-rubric judge) is sent as the user turn as is.
    }
    const args = [
      '-p',
      '--model', this.config.model,
      '--output-format', 'json',
      '--tools', '',
      '--setting-sources', '',
      '--strict-mcp-config',
      '--disable-slash-commands',
      '--no-session-persistence',
    ];
    if (this.config.effort) args.push('--effort', this.config.effort);
    if (system) args.push('--system-prompt', system);

    const started = Date.now();
    const res = await run('claude', args, user, this.config.timeoutMs || 600000);
    const latencyMs = Date.now() - started;
    if (res.code !== 0 && !res.stdout) {
      return { error: `claude -p exited ${res.code}: ${res.stderr.slice(-500)}` };
    }
    let d;
    try {
      d = JSON.parse(res.stdout);
    } catch {
      return { error: `unparseable claude -p output: ${res.stdout.slice(-500)}` };
    }
    if (d.is_error) return { error: `claude -p error: ${d.result || d.subtype}` };
    const u = d.usage || {};
    const promptTokens = (u.input_tokens || 0) + (u.cache_read_input_tokens || 0) + (u.cache_creation_input_tokens || 0);
    return {
      output: d.result || '',
      latencyMs: d.duration_ms || latencyMs,
      cost: d.total_cost_usd,
      tokenUsage: {
        prompt: promptTokens,
        completion: u.output_tokens || 0,
        total: promptTokens + (u.output_tokens || 0),
      },
    };
  }
}

function run(cmd, args, stdin, timeoutMs) {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, { cwd: os.tmpdir(), stdio: ['pipe', 'pipe', 'pipe'] });
    let stdout = '';
    let stderr = '';
    const timer = setTimeout(() => child.kill('SIGKILL'), timeoutMs);
    child.stdout.on('data', (b) => { stdout += b; });
    child.stderr.on('data', (b) => { stderr += b; });
    child.on('close', (code) => {
      clearTimeout(timer);
      resolve({ code, stdout, stderr });
    });
    child.stdin.end(stdin);
  });
}

module.exports = ClaudeCliProvider;
