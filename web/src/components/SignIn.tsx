/** signInHref is the server's sign-in, returning to the page the visitor is on. */
export function signInHref(path = location.pathname): string {
  return `/auth/login?next=${encodeURIComponent(path)}`;
}

/** SignOut ends the session. It is a POST form (the server refuses a GET,
 *  so a link on another site cannot sign anyone out) dressed as a link. */
export function SignOut() {
  return (
    <form className="signout" method="post" action="/auth/logout">
      <button type="submit" className="link-button">
        Sign out
      </button>
    </form>
  );
}

/** SignIn is what a hosted server shows a visitor without a session. */
export function SignIn({ forge }: { forge: string }) {
  return (
    <div className="gate">
      <div className="gate-card">
        <h1>aenotes</h1>
        <p className="muted">Collaborative whiteboards. Sign in to see the boards you have access to.</p>
        <a className="primary button" href={signInHref()}>
          Sign in with {forge}
        </a>
      </div>
    </div>
  );
}

/** NoAccess is a signed-in visitor whose account cannot read the board
 *  repository. */
export function NoAccess({ login, repo }: { login: string; repo?: string }) {
  return (
    <div className="gate">
      <div className="gate-card">
        <h1>No access</h1>
        <p>
          <strong>{login}</strong> has no access to the boards
          {repo ? (
            <>
              {' '}
              in <code>{repo}</code>
            </>
          ) : null}
          . Ask a maintainer of the repository for access, or sign in with another account.
        </p>
        <SignOut />
      </div>
    </div>
  );
}
