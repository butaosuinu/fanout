import { useEffect, useState } from "react";
import { apiUrl } from "../lib/api";
import type { Snapshot } from "../lib/types";

export interface ConnState {
  up: boolean;
  label: string;
}

const POLL_INTERVAL_MS = 2000;

/* SSE ストリームを購読し、切断時は /api/snapshot の 2 秒ポーリングへフォール
 * バックする。EventSource 未対応環境は最初からポーリング。SSE が再接続できた
 * 場合(EventSource の自動リトライ)はポーリングを止めてストリームに戻る。 */
export function useSnapshot(token: string): { snap: Snapshot | null; conn: ConnState } {
  const [snap, setSnap] = useState<Snapshot | null>(null);
  const [conn, setConn] = useState<ConnState>({ up: true, label: "connecting…" });

  useEffect(() => {
    let disposed = false;
    let pollTimer: ReturnType<typeof setInterval> | null = null;

    const stopPolling = () => {
      if (pollTimer) {
        clearInterval(pollTimer);
        pollTimer = null;
      }
    };
    const startPolling = () => {
      if (disposed || pollTimer) return;
      const tick = async () => {
        try {
          const res = await fetch(apiUrl("/api/snapshot", token), { cache: "no-store" });
          if (disposed) return;
          if (res.ok) {
            const body = (await res.json()) as Snapshot;
            if (disposed) return;
            setConn({ up: true, label: "polling" });
            setSnap(body);
          } else {
            setConn({ up: false, label: `error ${res.status}` });
          }
        } catch {
          if (!disposed) setConn({ up: false, label: "offline" });
        }
      };
      void tick();
      pollTimer = setInterval(() => void tick(), POLL_INTERVAL_MS);
    };

    setConn({ up: true, label: "linking…" });
    if (typeof EventSource === "undefined") {
      startPolling();
      return () => {
        disposed = true;
        stopPolling();
      };
    }

    let es: EventSource;
    try {
      es = new EventSource(apiUrl("/api/stream", token));
    } catch {
      startPolling();
      return () => {
        disposed = true;
        stopPolling();
      };
    }
    // 各リスナーの disposed ガード: cleanup(close)後にキュー済みイベントが
    // 届いても、誰も止めないポーリング interval を孤児として残さないため。
    es.addEventListener("open", () => {
      if (disposed) return;
      stopPolling();
      setConn({ up: true, label: "streaming" });
    });
    es.addEventListener("snapshot", (e) => {
      if (disposed) return;
      setConn({ up: true, label: "streaming" });
      setSnap(JSON.parse((e as MessageEvent).data) as Snapshot);
    });
    es.addEventListener("error", () => {
      if (disposed) return;
      setConn({ up: false, label: "stream lost" });
      es.close();
      startPolling();
    });

    return () => {
      disposed = true;
      es.close();
      stopPolling();
    };
  }, [token]);

  return { snap, conn };
}
