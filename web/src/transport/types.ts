/* Go の JSON DTO から生成した generated/*.gen.ts がワイヤ型の正。
 * make gen-types で更新する。アプリ側の import パスと型名はここで維持する。 */

export type {
  Snapshot,
  Session,
  PaneView,
  PaneDerived,
  PaneSortKeys,
  Rollup,
  Degraded,
} from "./generated/sessionview.gen";
export type { PRRef, PRStack, PRStackEntry } from "./generated/ghissue.gen";
export type { Status as BlockerStatus } from "./generated/blockers.gen";
export type {
  PeekResponse,
  PlanResponse,
  DiffResponse,
  DiffFileEntry,
} from "./generated/dashboard.gen";

import type { DiffFileEntry } from "./generated/dashboard.gen";
export type DiffOmittedReason = DiffFileEntry["omittedReason"];
