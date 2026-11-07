# Owned-process fixtures

採取日: **2026-10-27**。既存ユーザープロセスの argv は採取していない。
同じ列指定を macOS と Linux で実行し、対象をそのコマンドが生成した `/bin/sleep 30` の PID 1 個に限定した。

```sh
LC_ALL=C ps -p "$owned_sleep_pid" -o pid=,ppid=,pgid=,lstart=,comm=,args=
lsof -a -d cwd -p "$owned_sleep_pid" -Fn
```

`ps -axo ...` の全プロセス走査は実行していない。fixtures は列の出力形を固定するもので、全件走査や agent 分類の実機試験ではない。
`lstart` はこの形式では timezone を含まない。`LC_ALL=C` だけでは UTC にならないため、procStart の UTC 値と比較する実装は timezone も明示する必要がある。

| ファイル | 採取元 |
| --- | --- |
| `ps-macos.txt`, `lsof-macos.txt`, `macos-metadata.json` | macOS **15.6 arm64**、システム `/bin/ps` と `/usr/sbin/lsof`。専用 HOME / XDG roots / cwd を使う sleep を採取し、終了・回収した |
| `ps-linux.txt`, `linux-metadata.json` | GitHub Actions **ubuntu-24.04**、kernel **6.17.0-1022-azure x86_64**、**procps-ng 4.0.4**。checkout なし、新規一時 HOME / cwd、`LC_ALL=C`, `TZ=UTC` で sleep を採取し、finally で終了・回収した |

Linux の [採取 run](https://github.com/butaosuinu/fanout/actions/runs/40462777677) と [一時 workflow の取得元 commit](https://github.com/butaosuinu/fanout/blob/7e2fc7f0df9b9a7ebc8efa42a97474ad5917a042/.github/workflows/issue-866-capture.yml) を固定する。
artifact `issue-866-linux-proc` の ZIP は metadata に記録した SHA-256 と一致した。
一時 workflow は採取後に削除し、最終 PR 差分に含めない。
artifact の保持は 1 日なので、正規化済みの内容をこのディレクトリで保持する。

PID は 42001、親 PID は 42000 に置換した。
macOS では親と process group が同じなので PGID も 42000、Linux では別だったので 42002 とした。
数値は各列幅に右寄せし、残りの空白・lstart・comm・args を保持した。
cwd は `/fixture/work` または `/fixture/work with spaces` に置換し、時刻は実測値を保持した。
`comm` は macOS が `/bin/sleep`、Linux が `sleep`。`args` は両方 `/bin/sleep 30`。

macOS の `lsof` は 5 回とも exit 0、14.691–18.414 ms（個別値は metadata）。
1 PID の新規プロセスに対する値であり、多数 PID の性能保証には使わない。
`-Fn` でも `p` / `f` / `n` 行が出ることを確認した。
Linux の lsof、`/proc/<pid>/cwd`、プロセス消失・PID 再利用・特殊な argv / path の出力形は未採取。これらを実測済みの v1 契約として使わない。
