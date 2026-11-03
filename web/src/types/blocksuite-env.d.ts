// Globals BlockSuite's published TypeScript sources rely on but do not ship
// declarations for (its monorepo declares them in a private tsconfig scope).
// @blocksuite/* packages export their src/*.ts, so `tsc` checks them too.
interface VirtualKeyboardEventMap {
  geometrychange: Event;
}
interface VirtualKeyboard extends EventTarget {
  readonly boundingRect: DOMRect;
  overlaysContent: boolean;
  show(): void;
  hide(): void;
  addEventListener<K extends keyof VirtualKeyboardEventMap>(
    type: K,
    listener: (this: VirtualKeyboard, ev: VirtualKeyboardEventMap[K]) => unknown,
    options?: boolean | AddEventListenerOptions,
  ): void;
  removeEventListener<K extends keyof VirtualKeyboardEventMap>(
    type: K,
    listener: (this: VirtualKeyboard, ev: VirtualKeyboardEventMap[K]) => unknown,
    options?: boolean | EventListenerOptions,
  ): void;
}
interface Navigator {
  readonly virtualKeyboard?: VirtualKeyboard;
}
