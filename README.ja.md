# slack-mcp-extender

> **Status: リリース前 scaffold。** 設計は確定済み（[RFP](docs/ja/slack-mcp-extender-rfp.ja.md) 参照）
> ですが、proxy / OAuth / upload の実装は開発中です。以下の内容はまだ動作しません。

Claude の**純正 Slack MCP**（`mcp.slack.com/mcp`）を**完全透過でプロキシ**しつつ、
純正に欠けている唯一の機能 — **ファイル添付投稿** — を注入する、ワークスペース
単位の MCP プロキシです。

Claude 側からは 1 つの Slack コネクタに見え、純正の全ツールは無改変で素通し。
そこに注入ツールが 2 本加わります:

| ツール | 投稿形態 |
|---|---|
| `upload_file` | チャンネルのルートメッセージとして添付投稿 |
| `upload_file_to_thread` | スレッド返信として添付投稿（`thread_ts`） |

アップロードは Slack external upload 3-step を、プロキシが既に保持している
**同一の user token** で実行します — OAuth セッション 1 つ、identity 1 つ、
第 2 の credential は不要です。

## なぜ本人名義か（意図的な差異）

chatops-series の他ツール（swrite, stail, slack-router）は bot 認証ですが、
本ツールは意図的に **user token** を使います。純正 Slack コネクタ（＝あなた
本人として動作する）の拡張である以上、投稿されるファイルも本人名義であるべき
だからです。bot 名義のアップロードには [swrite](https://github.com/nlink-jp/swrite)
を使ってください。

## セキュリティモデル

本ツールは、非信頼な Slack コンテンツを中継し、ローカルファイルを読み、外部へ
送信します — 制約がなければデータ持ち出し（exfiltration）プリミティブです。
そのためファイルアクセスは operator が設定する **`allowed_roots`** に封じ込めます:

- canonical 化（Abs + Clean + EvalSymlinks）+ 包含チェック、deny-by-default
- roots 配下でも隠しパス成分（`.git`, `.env`, `.ssh` 等）は拒否
- 通常ファイルのみ・サイズ上限・構造化 `path_denied` エラー・監査ログ
- 封じ込めの規定は **operator の config のみ** — ツール引数や Slack 由来の値
  からは決して導出しない

## インストール

```bash
make build   # dist/slack-mcp-extender に出力（`go build` 直叩き禁止）
make test    # go test -race -cover ./...
```

署名・notarize 済みのビルド済みバイナリは v0.1.0 リリース時に Releases ページで
公開予定です。

## 使い方（予定インターフェース）

```bash
slack-mcp-extender init                    # ワークスペース config を対話的に作成
slack-mcp-extender login --config <path>   # OAuth（ワークスペースごとに 1 回）
slack-mcp-extender mcp --config <path>     # stdio MCP サーバー起動
```

Slack の user token はワークスペース単位です: **ワークスペースごとに config と
Claude Desktop の MCP 登録を 1 つずつ**作成してください。

## ドキュメント

- [RFP（日本語）](docs/ja/slack-mcp-extender-rfp.ja.md) /
  [RFP (English)](docs/en/slack-mcp-extender-rfp.md)

## ライセンス

[MIT](LICENSE)
