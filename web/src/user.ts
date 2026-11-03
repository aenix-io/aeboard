// The visitor's display name for presence (cursors). Until sign-in lands it
// is chosen locally and remembered in this browser.
const KEY = 'aeboard.name';

export function loadName(): string {
  try {
    const v = localStorage.getItem(KEY);
    if (v) return v;
  } catch {
    /* storage blocked */
  }
  return `Guest ${Math.floor(100 + Math.random() * 900)}`;
}

export function saveName(name: string): void {
  try {
    localStorage.setItem(KEY, name);
  } catch {
    /* storage blocked */
  }
}
