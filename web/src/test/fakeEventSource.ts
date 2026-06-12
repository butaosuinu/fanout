import { vi } from "vitest";

/* jsdom に EventSource が無いので、テストから手動で open / snapshot / error を
 * 発火できる代替を global 注入する。アプリ側(useSnapshot)は本物と同じ API
 * (addEventListener / onerror / close)しか使わない。 */
export class FakeEventSource {
  static instances: FakeEventSource[] = [];

  static latest(): FakeEventSource {
    const es = FakeEventSource.instances.at(-1);
    if (!es) throw new Error("no FakeEventSource instantiated");
    return es;
  }

  url: string;
  closed = false;
  onerror: ((e: Event) => void) | null = null;
  private listeners = new Map<string, Set<(e: MessageEvent) => void>>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, fn: (e: MessageEvent) => void): void {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type)!.add(fn);
  }

  removeEventListener(type: string, fn: (e: MessageEvent) => void): void {
    this.listeners.get(type)?.delete(fn);
  }

  close(): void {
    this.closed = true;
  }

  /* ---- テスト用ヘルパー ---- */

  emitOpen(): void {
    this.dispatch("open", new MessageEvent("open"));
  }

  emitSnapshot(snap: unknown): void {
    this.dispatch("snapshot", new MessageEvent("snapshot", { data: JSON.stringify(snap) }));
  }

  emitError(): void {
    this.onerror?.(new Event("error"));
  }

  private dispatch(type: string, e: MessageEvent): void {
    for (const fn of this.listeners.get(type) ?? []) fn(e);
  }
}

export function installFakeEventSource(): void {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
}

export function removeEventSource(): void {
  // EventSource 未対応環境(即ポーリング)を再現する
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", undefined);
}
