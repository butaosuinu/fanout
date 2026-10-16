import { useLingui } from "@lingui/react/macro";
import { Fragment, type ReactNode } from "react";
import type { PaneView, PRRef } from "../../transport/types";
import { PrPill } from "../sessions/badges";
import { paneLabel, paneName } from "../sessions/pane";
import { sameRepo, type StackLayer, type StackView } from "../sessions/stack";
import { useSelectStackOwner } from "../sessions/StackIndex";

/* ドロワーの stack map。上の層ほど上に並べ、底に trunk(stack の base)を置く。
 *
 * この行の PR は renderOwn で描く — 行のコピーには CI・競合・コメントと Delete
 * branch が付く。Delete branch はサーバが行の head と照合するので、他の行や stack
 * 取得のコピーでは描かない。他の行の層は状態ピルとその行の名前だけで、名前を
 * 押すとその行のドロワーへ切り替わる。 */
export function StackMap({
  view,
  pane,
  repo,
  renderOwn,
}: {
  view: StackView;
  pane: PaneView;
  repo: string;
  renderOwn: (pr: PRRef) => ReactNode;
}) {
  return (
    <li className="d-stack">
      <StackHeading view={view} />
      <ol className="d-stack-layers">
        {[...view.layers]
          .sort((a, b) => b.position - a.position)
          .map((layer) => (
            <StackLayerRow
              key={layer.pr.number}
              layer={layer}
              own={pane.prs?.find(
                (p) => p.number === layer.pr.number && sameRepo(p.baseRepo, repo),
              )}
              repo={repo}
              renderOwn={renderOwn}
            />
          ))}
      </ol>
      <div className="d-stack-trunk">{view.baseRef}</div>
    </li>
  );
}

function StackHeading({ view }: { view: StackView }) {
  const { t } = useLingui();
  const { number, size, baseRef } = view;
  const label =
    view.kind === "native"
      ? t`stack #${number} → ${baseRef} · ${size} 層`
      : t`base 連鎖(推定)→ ${baseRef} · ${size} 層`;
  return <div className="d-stack-head">{label}</div>;
}

function StackLayerRow({
  layer,
  own,
  repo,
  renderOwn,
}: {
  layer: StackLayer;
  /* この行が持つ同じ PR のコピー。無ければ他の行の層。 */
  own: PRRef | undefined;
  repo: string;
  renderOwn: (pr: PRRef) => ReactNode;
}) {
  const { t } = useLingui();
  const selectOwner = useSelectStackOwner();
  if (own) {
    return (
      <li className="d-stack-layer self" aria-current="true">
        <span className="d-stack-pos">{layer.position}</span>
        {renderOwn(own)}
        <span className="d-stack-self">{t`◀ この Session`}</span>
      </li>
    );
  }
  return (
    <li className="d-stack-layer">
      <span className="d-stack-pos">{layer.position}</span>
      <PrPill repo={repo} pr={layer.pr} />
      {layer.owners.length > 0 && (
        <span className="muted">
          {layer.owners.map((o, i) => (
            <Fragment key={`${i}:${paneLabel(o)}`}>
              {i > 0 && ", "}
              <button
                type="button"
                className="diff-link"
                title={t`この行の詳細を開く`}
                onClick={() => selectOwner(o)}
              >
                {paneName(o) || paneLabel(o)}
              </button>
            </Fragment>
          ))}
        </span>
      )}
    </li>
  );
}
