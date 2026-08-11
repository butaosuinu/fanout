/* localStorage の読み書き。private mode や quota 超過で例外を投げるので、書けな
 * かった値はここに退避し、read が storage より優先して返す — そうしないと呼び出し側
 * が storage を読み直すたびに旧値へ戻り、操作が実質 no-op になる。書けた場合は
 * storage が正なので退避を捨てる。
 *
 * 保存値は敵性入力として扱う(別タブ・拡張・手動編集で任意の文字列が入る)。
 * 検証は値の意味を知っている呼び出し側の責務で、ここは素の string しか返さない。 */
const unpersisted = new Map<string, string | null>();

export function readLocal(key: string): string | null {
  if (unpersisted.has(key)) return unpersisted.get(key) ?? null;
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeLocal(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
    unpersisted.delete(key);
  } catch {
    unpersisted.set(key, value); // このタブのあいだだけ効かせる
  }
}

/* 接頭辞に一致する保存済みキー。剪定のために「いま何本あるか」を数える用途で、
 * 列挙中の例外(private mode)は空配列に落とす。退避中(書けなかった)キーは
 * storage に無いので現れない — 剪定の対象にもならず、それでよい。 */
export function localKeysWithPrefix(prefix: string): string[] {
  try {
    /* Storage の own enumerable プロパティは保存済みキーそのもの(getItem 等の
     * メソッドは prototype 側なので現れない)。 */
    return Object.keys(localStorage).filter((k) => k.startsWith(prefix));
  } catch {
    return [];
  }
}
