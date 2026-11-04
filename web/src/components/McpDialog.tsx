import { useEffect, useState } from 'react';

/** McpDialog shows how to connect an AI agent to these notes over MCP: the
 *  Claude Code command and the JSON config other agents take. */
export function McpDialog({ hosted, onClose }: { hosted: boolean; onClose: () => void }) {
  const url = `${window.location.origin}/mcp`;
  const command = `claude mcp add --transport http aenotes ${url}`;
  const config = JSON.stringify({ mcpServers: { aenotes: { type: 'http', url } } }, null, 2);
  // "copied" stays on the block copied last; the dialog unmounts on close.
  const [copied, setCopied] = useState<string | null>(null);
  const copy = (key: string, text: string) => {
    void navigator.clipboard?.writeText(text);
    setCopied(key);
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const block = (key: string, text: string) => (
    <div className="mcp-block">
      <pre>
        <code>{text}</code>
      </pre>
      <button type="button" className="mcp-copy" onClick={() => copy(key, text)}>
        {copied === key ? 'Copied' : 'Copy'}
      </button>
    </div>
  );

  return (
    <div className="dialog-scrim" onClick={onClose}>
      <div className="dialog mcp-dialog" role="dialog" aria-modal="true" aria-label="Connect an agent" onClick={e => e.stopPropagation()}>
        <h2>Connect an agent (MCP)</h2>
        <p className="mcp-lead">
          Agents can list, search and read your pages and boards, create documents and add to them. Changes show at
          once for everyone and are committed to git in your name.
        </p>
        <h3>Claude Code</h3>
        {block('cmd', command)}
        <h3>Other agents (JSON config)</h3>
        {block('json', config)}
        <p className="mcp-note">
          {hosted
            ? 'On first use the agent opens a browser to sign in with GitHub. It gets the same access you have here.'
            : 'This server runs locally: no sign-in, the agent acts as the local identity.'}
        </p>
        <div className="mcp-actions">
          <button type="button" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}
