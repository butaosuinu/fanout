import { Component, type ReactNode } from "react";

/* lazy chunk の取得失敗を受け止める境界。
 *
 * `Suspense` は解決待ちしか扱わない。`import()` が reject すると例外がルートまで
 * 抜け、React は木ごと unmount する — diff を初めて開いた操作で dashboard 全体が
 * 白紙になる。ダッシュボードは開きっぱなしで使うものなので、これは現実に起きる:
 * 固定 port で再起動すると新しいビルドのハッシュ付き chunk に入れ替わり、開いた
 * ままのページが持つ古い URL は 404 になる(一時的なネットワーク断でも同じ)。
 *
 * リトライは境界ごと作り直す側の責任にする(diff は閉じて開き直す = この境界が
 * unmount → mount される)。境界自身に再試行ボタンを持たせても、React は失敗した
 * lazy コンポーネントの結果をモジュール単位でキャッシュするため同じ例外が返る。 */
export class ChunkBoundary extends Component<
  { children: ReactNode; fallback: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: unknown) {
    // 開発者向けのログなので翻訳しない(画面には出ない)
    console.error("chunk の読み込みに失敗しました", error);
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}
