import { useEffect, useState } from "react";
import { apiUrl } from "./api";
import type { Snapshot } from "./types";

export interface ConnState {
  up: boolean;
  label: string;
}

const POLL_INTERVAL_MS = 2000;

/* ポーリング 1 回分の結果。失敗も例外ではなく接続状態として返す。 */
interface PollResult {
  conn: ConnState;
  snap?: Snapshot;
}

/* SSE イベントの配線先。error は「ストリームを畳んでポーリングへ落とす」役なので
 * 畳む対象の EventSource を受け取る。 */
interface StreamHandlers {
  open: () => void;
  snapshot: (e: Event) => void;
  lost: (es: EventSource) => void;
}

/* /api/snapshot を 1 回読む。 */
async function fetchSnapshot(token: string): Promise<PollResult> {
  try {
    const res = await fetch(apiUrl("/api/snapshot", token), { cache: "no-store" });
    if (!res.ok) return { conn: { up: false, label: `error ${res.status}` } };
    return { conn: { up: true, label: "polling" }, snap: (await res.json()) as Snapshot };
  } catch {
    return { conn: { up: false, label: "offline" } };
  }
}

/* EventSource を開く。未対応環境も生成失敗もどちらも null を返し、呼び出し側は
 * 「ストリームを張れたか」の 1 分岐だけを見る。 */
function openStream(token: string): EventSource | null {
  if (typeof EventSource === "undefined") return null;
  try {
    return new EventSource(apiUrl("/api/stream", token));
  } catch {
    return null;
  }
}

/* SSE の 3 イベントを配線する。 */
function bindStream(es: EventSource, on: StreamHandlers): void {
  es.addEventListener("open", on.open);
  es.addEventListener("snapshot", on.snapshot);
  es.addEventListener("error", () => on.lost(es));
}

/* /api/snapshot の 2 秒ポーリング。start は既に走っていれば何もしないので、
 * 再入しても interval は 1 本のまま。 */
function createPoller(tick: () => Promise<void>): { start: () => void; stop: () => void } {
  let timer: ReturnType<typeof setInterval> | null = null;
  const start = () => {
    if (timer) return;
    void tick();
    timer = setInterval(() => void tick(), POLL_INTERVAL_MS);
  };
  const stop = () => {
    if (timer) clearInterval(timer);
    timer = null;
  };
  return { start, stop };
}

/* SSE ストリームを購読し、切断時は /api/snapshot の 2 秒ポーリングへフォール
 * バックする。EventSource 未対応環境は最初からポーリング。SSE が再接続できた
 * 場合(EventSource の自動リトライ)はポーリングを止めてストリームに戻る。 */
export function useSnapshot(token: string): { snap: Snapshot | null; conn: ConnState } {
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [conn, setConn] = useState<ConnState>({ up: true, label: "connecting…" });

  useEffect(() => {
    setConn({ up: true, label: "linking…" });
    // live は cleanup(close)後に届く応答とキュー済みイベントをまとめて弾く。
    // 弾かないと、誰も止めないポーリング interval を孤児として残す。
    let live = true;

    const refresh = async () => {
      const result = await fetchSnapshot(token);
      if (!live) return;
      setConn(result.conn);
      if (result.snap) setSnap(result.snap);
    };
    const poller = createPoller(refresh);

    const stream = openStream(token);
    if (stream) {
      bindStream(stream, {
        open: () => {
          if (!live) return;
          poller.stop();
          setConn({ up: true, label: "streaming" });
        },
        snapshot: (e) => {
          if (!live) return;
          setConn({ up: true, label: "streaming" });
          setSnap(JSON.parse((e as MessageEvent).data) as Snapshot);
        },
        lost: (es) => {
          if (!live) return;
          setConn({ up: false, label: "stream lost" });
          es.close();
          poller.start();
        },
      });
    } else {
      poller.start();
    }

    return () => {
      live = false;
      stream?.close();
      poller.stop();
    };
  }, [token]);

  return { snap, conn };
}
