import { FileDiff } from "@pierre/diffs/react";
import type { Theme } from "../hooks/useSettings";
import { parseDiffFiles } from "../lib/diff";
import { DIFF_THEME_SAMPLE_PATCH } from "../lib/diffThemes";

/* 設定モーダルの diff テーマ見本。本番と同じ <FileDiff> で描くので、選んだ
 * テーマの配色がそのまま見える(自前で色を再現しない = drift しない)。
 *
 * このファイルは App から lazy() 経由でのみ読まれる。@pierre/diffs(Shiki 込み)
 * を初回ロードのパスに乗せない、という DiffOverlay と同じ理由。 */

/* 見本の patch は固定リテラルなので、パースはモジュール初期化時に 1 回だけ。 */
const SAMPLE = parseDiffFiles(DIFF_THEME_SAMPLE_PATCH)[0];

export function DiffThemePreview({ name, themeType }: { name: string; themeType: Theme }) {
  if (!SAMPLE) return null;
  return (
    <div className="set-theme-preview">
      <FileDiff
        fileDiff={SAMPLE}
        options={{
          /* light/dark の両方に同じ名前を入れて themeType で固定する。ページの
             外観が dark でも、ライト用テーマの見本はライトのまま出したい。 */
          theme: { light: name, dark: name },
          themeType,
          /* 狭い 2 カラムに収めるので unified。ファイル名は見本には要らない。
             hunkSeparators は既定(line-info)のまま — "simple" を渡すと本文が
             一切描かれない(v1.2.12 で実測)。 */
          diffStyle: "unified",
          disableFileHeader: true,
          collapsed: false,
        }}
      />
    </div>
  );
}

export default DiffThemePreview;
