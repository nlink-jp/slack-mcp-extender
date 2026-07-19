# slack-mcp-extender

> 実装・単体テスト済みで、**実ワークスペースでの E2E 検証も完了**しています
> （実 proxy の透過動作・root/thread 添付投稿・封じ込め拒否と監査記録）。
> macOS バイナリは Developer ID 署名 + notarize 済み。設計は
> [RFP](docs/ja/slack-mcp-extender-rfp.ja.md) 参照。

Claude の**純正 Slack MCP**（`mcp.slack.com/mcp`）を**完全透過でプロキシ**しつつ、
純正に欠けている機能 — **Slack とローカルディスク間の実ファイル移動** — を
注入する、ワークスペース単位の MCP プロキシです。

Claude 側からは 1 つの Slack コネクタに見え、純正の全ツールは無改変で素通し。
そこに `ext_` 名前空間の注入ツールが 3 本加わります（公式 `slack_*` ツールと
構造的に衝突しません）:

| ツール | 動作 |
|---|---|
| `ext_file_upload` | ローカルファイルをルートメッセージとして添付投稿 |
| `ext_file_upload_to_thread` | ローカルファイルをスレッド返信として添付投稿（`thread_ts`） |
| `ext_file_download` | Slack ファイル（`file_id`）をローカルに保存 — 上書きは絶対にしない |

転送はプロキシが既に保持している**同一の user token** で実行します —
OAuth セッション 1 つ、identity 1 つ、第 2 の credential は不要です。

## なぜ本人名義か（意図的な差異）

chatops-series の他ツール（swrite, stail, slack-router）は bot 認証ですが、
本ツールは意図的に **user token** を使います。純正 Slack コネクタ（＝あなた
本人として動作する）の拡張である以上、投稿されるファイルも本人名義であるべき
だからです。bot 名義のアップロードには [swrite](https://github.com/nlink-jp/swrite)
を使ってください。

## セキュリティモデル

本ツールは、非信頼な Slack コンテンツを中継し、ローカルファイルを読み書きし、
データを双方向に移動します — 制約がなければ、出る方向は exfiltration、入る
方向は書き込みプリミティブです。そのため**双方向とも**ファイルアクセスを
operator が設定する **`allowed_roots`** に封じ込めます:

- canonical 化（Abs + Clean + EvalSymlinks）+ 包含チェック、deny-by-default
- roots 配下でも隠しパス成分（`.git`, `.env`, `.ssh` 等）は拒否
- 通常ファイルのみ・サイズ上限（申告サイズ**と**転送中の実測の両方）・構造化
  `path_denied` エラー・egress/ingress 監査ログ
- download は上書き不可、Slack 側ファイル名が影響できるのは（サニタイズ後の）
  保存名のみで、置き場所には決して影響しない
- 封じ込めの規定は **operator の config のみ** — ツール引数や Slack 由来の値
  からは決して導出しない

## インストール

[Releases](https://github.com/nlink-jp/slack-mcp-extender/releases) から
各プラットフォームの最新バイナリをダウンロード（macOS ビルドは Developer ID
署名 + notarize 済み）、またはソースからビルド:

```bash
make build   # dist/slack-mcp-extender に出力（`go build` 直叩き禁止）
make test    # go test -race -cover ./...
```

## セットアップ

初めての場合は **[Slack セットアップガイド](docs/ja/slack-setup.ja.md)** を
参照してください — 同梱の [App マニフェスト](docs/slack-app-manifest.yaml)
からの Slack App 作成、ワークスペース config の作成
（[config.example.json](config.example.json) から開始）、ログイン、
Claude Desktop への登録までを段階的に説明しています。

```bash
slack-mcp-extender init                              # ワークスペース config を対話的に生成
slack-mcp-extender config validate --config <path>   # config の検証
slack-mcp-extender login --config <path>             # OAuth（ワークスペースごとに 1 回）
slack-mcp-extender mcp --config <path>               # stdio MCP サーバー起動
```

`init` は OAuth クライアント情報・secret の保管方法（環境変数推奨）・
allowed roots を対話的に聞き、config（0600）を書き出して、login コマンドと
Claude Desktop 登録スニペットを表示します。手書きよりこちらを推奨。
フィールドの全容は [config.example.json](config.example.json) を参照。

`--config` はフルパスのほか、**ワークスペース名だけ**でも指定できます —
`~/.config/slack-mcp-extender` 内で解決され、`.json` は自動補完されます
（例: `login --config myworkspace` → 同ディレクトリの `myworkspace.json`）。

Slack の user token はワークスペース単位です: **ワークスペースごとに config と
Claude Desktop の MCP 登録を 1 つずつ**作成してください。

## ドキュメント

- [Slack セットアップガイド](docs/ja/slack-setup.ja.md)
  （[English](docs/en/slack-setup.md)）—
  App マニフェスト: [docs/slack-app-manifest.yaml](docs/slack-app-manifest.yaml)
- [RFP（日本語）](docs/ja/slack-mcp-extender-rfp.ja.md) /
  [RFP (English)](docs/en/slack-mcp-extender-rfp.md)

## ライセンス

[MIT](LICENSE)
