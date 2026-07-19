# ADR-0001: 注入ツールの `ext_` 名前空間

> Status: Accepted — 2026-07-20
> RFP に記録されたツール名（`upload_file`, `upload_file_to_thread`）を置き換える。

## 背景

v0.1.0 の注入ツールは `upload_file` / `upload_file_to_thread` という汎用名で、
マージ後の tools/list 上で純正 Slack MCP ツール（`slack_*`）と見分けがつかない。

問題は 2 つ:

1. **衝突による公式機能の隠蔽。** プロキシは名前衝突を注入（ローカル）側優先で
   決定的に解決する。汎用名のままだと、将来 Slack が公式に `upload_file` を
   出した場合、その新機能を静かに隠してしまう — 「拡張はするが改変しない」と
   いう本ツールの思想の真逆の挙動になる。
2. **帰属の不明瞭さ。** エージェントも人間も、ツール一覧やログからどれが公式で
   どれが拡張かを判別できない。

## 決定

注入ツールを明示的な `ext_` 名前空間に置く:

| v0.1.0 | v0.2.0 |
|---|---|
| `upload_file` | `ext_file_upload` |
| `upload_file_to_thread` | `ext_file_upload_to_thread` |
| — | `ext_file_download`（ADR-0002） |

このリネームは破壊的変更だが、互換エイリアスなしの v0.2.0 として出す:
v0.1.0 は前日リリースで、既知の利用者はリネームを提案した operator 本人のみ。
衝突処理の機構自体は defense in depth として残す。

## 帰結

- upstream 名前空間（`slack_*`）と拡張名前空間（`ext_*`）は構造的に衝突せず、
  公式追加が隠蔽されることがなくなる。
- ツールの出自が tools/list とログの全行で自己記述的になる。
- 今後の注入ツールは必ず `ext_` 接頭辞を付ける。
