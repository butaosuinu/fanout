/* @lingui/vite-plugin は *.po の ambient 型を配らない (exports は "." のみ) ので
 * 自前で宣言する。カタログの実体は lingui() プラグインが生成する。 */
declare module "*.po" {
  import type { Messages } from "@lingui/core";

  export const messages: Messages;
}
