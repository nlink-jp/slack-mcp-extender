# Slack セットアップガイド

slack-mcp-extender を 1 つの Slack ワークスペースで動かすまでの手順です:
Slack App の作成（または再利用）、ワークスペース config の作成、ログイン、
Claude Desktop への MCP 登録。

Slack の user token は**ワークスペース単位**です。使いたいワークスペースごとに
手順 3–6 を繰り返してください。Slack App 自体（手順 1–2）は共有できます。

## 1. Slack App を作成する

[`docs/slack-app-manifest.yaml`](../slack-app-manifest.yaml) のスコープで
**user token** を発行できる Slack App が必要です。マニフェストのスコープ
セットは**純正 Slack MCP サーバーの全ツール**（検索・メッセージ読み書き・
会話作成・リアクション・Canvas・ファイル読み取り/DL・絵文字・プロフィール —
[公式のツール⇄スコープ表](https://docs.slack.dev/ai/slack-mcp-server/)準拠）
＋ 注入 upload ツール用の `files:write` をカバーします。各スコープがどの
ツールを有効にするかはマニフェストのコメントに対応表があるので、意図的に
削る際の参考にしてください。スコープが欠けると該当 upstream ツールが
`missing_scope` エラーになります。Slack の MCP エンドポイントは internal
（ワークスペースインストール）App で利用可能 — ディレクトリ公開は不要です。

**方法 A — 専用 App を新規作成（クリーンに始めるなら推奨）:**

1. <https://api.slack.com/apps> → **Create New App** → **From a manifest**
2. 対象ワークスペースを選択
3. [`docs/slack-app-manifest.yaml`](../slack-app-manifest.yaml) の内容を
   貼り付けて作成

**方法 B — 既存 App を再利用**（他の CLI で使用中の App など）: App 設定で
以下を確認・追加します。

- **OAuth & Permissions → Scopes → User Token Scopes**: マニフェスト記載の
  一式 — upload ツールに必須なのは `files:write`
- **OAuth & Permissions → Redirect URLs**: `https://localhost:7777/callback`
  （config の `oauth.callback_port` と一致させること）
- **MCP 有効化**: App で MCP が有効になっていること（マニフェストでいう
  `settings.is_mcp_enabled: true` — App の設定/マニフェストで確認）。
  無効だと Slack の MCP エンドポイントが接続を拒否します。その App が既に
  別の MCP proxy で `mcp.slack.com` に接続できているなら有効化済みです。

config が**要求**するスコープは、App が**許可**するスコープの部分集合で
ある必要があります — App 側に無いスコープを要求すると authorize の段階で
失敗します。App 側に不足分を追加するか、config の `oauth.scopes` を App の
許可範囲まで削ってください（削った分の upstream ツールは使えなくなります）。

> 認可済み App へのスコープ追加は、各ワークスペースでの**再認可**（手順 5）が
> 必要です。再認可で user token がローテートすることがあります。この App を
> 別ツールが独自のトークン保存で共有している場合は、再認可後にそちらの動作を
> 確認してください。

## 2. クライアント credential を控える

App の **Basic Information → App Credentials** で **Client ID** と
**Client Secret** を確認します。secret は環境変数に入れ、コミットされる
ファイルには絶対に書かないでください。

## 3. ワークスペース config を書く

[`config.example.json`](../../config.example.json) をワークスペースごとに
1 ファイルずつコピーします:

```bash
mkdir -p ~/.config/slack-mcp-extender
cp config.example.json ~/.config/slack-mcp-extender/myworkspace.json
chmod 600 ~/.config/slack-mcp-extender/myworkspace.json
```

編集項目:

- `oauth.client_id` — 手順 2 の値
- `oauth.client_secret_env` — secret を保持する環境変数名（推奨）。リテラルで
  書く場合は `oauth.client_secret`（その場合 config ファイル自体が secret 保管庫
  になるので 0600 を維持）
- `oauth.scopes` — App が許可する user スコープと一致させ、`files:write` を
  含めること
- `allowed_roots` — upload ツールが読んでよい**絶対パス**のディレクトリ群。
  ここがセキュリティ境界です: roots の外は一切アップロードできず、roots 配下でも
  隠しエントリ（`.git`, `.env` など）は拒否され、空リストなら全ファイルアクセス
  拒否。動作する最小の範囲 — 専用の受け渡しディレクトリや、エージェントの
  セッション/出力領域など — を指定してください。

確認:

```bash
slack-mcp-extender config validate --config ~/.config/slack-mcp-extender/myworkspace.json
```

## 4. ログイン（ワークスペースごとに 1 回）

```bash
export SLACK_MCP_EXTENDER_CLIENT_SECRET='...'   # client_secret_env を使う場合
slack-mcp-extender login --config ~/.config/slack-mcp-extender/myworkspace.json
```

ブラウザで Slack の同意画面が開きます。想定内の挙動が 2 つ:

- 同意後、`https://localhost:7777/callback` に**自己署名証明書**で着地する
  ため「保護されていない通信」警告が出ます。そのまま進んでください。callback
  はローカルマシンから外に出ません。
- ポートが使用中の場合は `oauth.callback_port` と App 側 Redirect URL を
  **セットで**変更してください。

トークンは config の state ディレクトリ
（`<config名>.state/tokens.json`, 0600）に保存されます。

## 5. スコープ変更後の再認可

後からスコープを追加した場合（認可済み App への `files:write` 追加など）は、
**各**ワークスペースで `login` をやり直してください — トークンは新しい
スコープセットで再発行される必要があります。

## 6. Claude Desktop に登録する

Claude Desktop の MCP 設定に、**ワークスペースごとに 1 エントリ**追加します:

```json
{
  "mcpServers": {
    "slack-myworkspace": {
      "command": "/path/to/slack-mcp-extender",
      "args": ["mcp", "--config", "/Users/you/.config/slack-mcp-extender/myworkspace.json"]
    }
  }
}
```

Claude Desktop を再起動すると、純正 Slack MCP の全ツールがそのまま、加えて
`upload_file` と `upload_file_to_thread` が使えるようになります。

## トラブルシューティング

| 症状 | 原因 / 対処 |
|---|---|
| `no stored tokens (run ... login first)` | この config で手順 4 が未実施、または state ディレクトリが移動された |
| `HTTP 401: authentication failed after token refresh` | トークン失効 — `login` をやり直す |
| ツールエラー `path_denied` | ファイルが `allowed_roots` の外に解決される（または隠し・サイズ超過・通常ファイル以外）。`details` にどのルールとどの roots かが入っている |
| ツールエラー `slack_api_error: not_in_channel` | 認可ユーザーが対象チャンネルに未参加 — 先に Slack 側で参加する |
| callback でブラウザ警告 | 想定内（自己署名 loopback TLS）— そのまま進む |
| 作成直後のチャンネルが検索ツールで見つからない | Slack の検索インデックスは新規チャンネルの反映が遅れる。チャンネル ID を直接指定するか、反映を待つ |
| `start callback server on port 7777` | ポート使用中 — `oauth.callback_port` と App の Redirect URL をセットで変更 |
