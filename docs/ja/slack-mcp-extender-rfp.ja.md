# RFP: slack-mcp-extender

> Generated: 2026-07-19
> Status: Draft

## 1. Problem Statement

Claude の純正 Slack コネクタ（`mcp.slack.com/mcp`）は、メッセージの読み取り・投稿・
ファイルのダウンロードはできるが、**ファイルの添付投稿ができない**。一方、Slack の
読み取りや通常のメッセージ投稿は純正コネクタ・既存ツール（swrite/scat/stail/scli 等）で
十分に賄えており、欠けているのは「ファイル添付」の一点のみである。

slack-mcp-extender は、純正 Slack MCP を**完全透過でプロキシ**しつつ、ファイル
アップロードツール（ルートメッセージへの添付投稿・スレッドへの添付投稿）だけを
**注入**する、ワークスペース単位の MCP プロキシである。Claude 上では従来どおり
1 つの Slack コネクタに見え、そこに添付機能が加わる。

対象ユーザーは、Claude Desktop（cowork 含む）で純正 Slack MCP を利用しつつ、
成果物ファイルを Slack に添付投稿したい運用者（当面は nlink-jp org の単独運営者）。

## 2. Functional Specification

### Commands / API Surface

CLI はライフサイクル管理に必要な最小限のみ（アップロードの CLI 実行は持たない —
bot 名義のアップロードは swrite が既に担う）。

| コマンド | 役割 |
|---|---|
| `slack-mcp-extender mcp --config <path>` | stdio MCP サーバー起動（透過プロキシ + ツール注入） |
| `slack-mcp-extender init` | config 雛形の対話的生成（allowed_roots の登録を含む）+ Claude Desktop 登録用 JSON スニペット出力 |
| `slack-mcp-extender login --config <path>` | OAuth authorization_code フロー実行・トークン保存（ワークスペースごとに 1 回） |
| `slack-mcp-extender config ...` | config の表示・検証 |

#### MCP 面

- **透過プロキシ**: upstream（`mcp.slack.com/mcp`, SSE）の全ツール・通知・レスポンスを
  無改変で中継する。ツール名の書き換えは行わない。
- **注入ツール 2 本**:

| ツール | 引数 | 動作 |
|---|---|---|
| `upload_file` | `channel`(必須), `file`(必須), `workspace_dir`(任意), `comment`(任意), `filename`(任意) | ファイルをアップロードし、チャンネルのルートメッセージとして添付投稿 |
| `upload_file_to_thread` | 上記 + `thread_ts`(必須) | ファイルをアップロードし、既存メッセージのスレッド返信として添付投稿 |

- 実装は Slack external upload 3-step（`files.getUploadURLExternal` → upload URL へ
  POST → `files.completeUploadExternal`）。`completeUploadExternal` に `channel_id`
  （+ `initial_comment` / `thread_ts`）を渡すことで、アップロードと投稿を 1 操作に
  閉じ、宙に浮いたファイルを作らない。
- tools/list マージ時に upstream 側と名前衝突を検出した場合は警告ログを出し、
  注入ツール側（ローカル）を優先して決定的にルーティングする。

### Input / Output

**ファイル入力（2 モード、単一の封じ込めに統一）**:

1. **workspace_dir モード**: エージェントがツール引数 `workspace_dir` で作業
   ディレクトリを指定し、`file` は相対パスとして解決する（voice-studio-mcp /
   video-studio-mcp と同型）。**workspace_dir はエージェント指定であることが必須**
   — cowork ではセッションの作業ディレクトリをエージェント側が握っており、
   config 固定ディレクトリでは動作不能になるため。
2. **直接パスモード**: `file` に絶対パスを指定する。

いずれのモードでも、解決後のパスは **canonical 化（Abs + Clean + EvalSymlinks）**
した上で、operator が config に設定した **`allowed_roots`** のいずれかの配下に
あることを検証する。`workspace_dir` 引数自体は非信頼入力であり、封じ込めの境界は
あくまで config 側の `allowed_roots` である。

- **deny-by-default**: `allowed_roots` 未設定時はすべてのファイル操作を拒否する。
  `init` が対話的に allowed_roots を登録する（cowork セッション親ディレクトリ等を
  候補として提示）。
- 通常ファイルのみ許可（ディレクトリ / allowed_roots 外へ解決される symlink /
  デバイス / ソケットは拒否）。
- **隠しパス成分の拒否（defense-in-depth）**: canonical 化後、マッチした
  allowed_root からの**相対部分**に `.` で始まる成分（`.git` / `.env` / `.ssh` /
  `.aws` 等、ディレクトリ・ファイルとも）を 1 つでも含むパスは拒否する。
  判定を root からの相対部分に限るのは、allowed_root 自体が dot ディレクトリ
  配下にある構成（cowork セッション親等）を壊さないため — root までの経路は
  operator が明示許可した範囲として信頼する。symlink 経由で dot パスへ解決される
  偽装も、EvalSymlinks 後の判定のため自動的に捕捉される。config の
  `allow_hidden`（既定 false）で opt-out 可能（統制は帯域外のみ）。
- config でサイズ上限を設定（既定 50MB、変更可。Slack 側の上限とは独立の自衛）。

**出力**:

- 成功: `{ ok: true, file_id, channel_id, filename, size }` を含む構造化結果。
- 失敗: 構造化エラー `{ code, message, details }`。パス拒否は
  `code: "path_denied"`、details に対象パスと allowed_roots を含める
  （隠し成分拒否は `details.reason: "hidden_component"`）。
- **最小監査ログ**: いつ / どのファイルを / どのチャンネル（スレッド）へ
  アップロードしたかを state ディレクトリに追記記録する（egress の記録）。

### Configuration

- **ワークスペース単位の config ファイル**（JSON、0600）。Slack の user token は
  ワークスペース単位のため、config・トークンストア・state ディレクトリ・
  allowed_roots をすべてワークスペースごとに分離する。
- Claude Desktop への **MCP 登録もワークスペース単位**（例: `slack-ext-<ws>`）。
  1 プロセスでの多重化は行わない（tools/list の名前衝突と透過性の破壊を招くため）。
- config 項目: upstream URL / OAuth（authorizeUrl, tokenUrl, clientId, clientSecret,
  scopes, callback 設定）/ allowed_roots / max_file_size / state_dir。
- OAuth クライアント（clientId/clientSecret）は config 間で共有可。トークンと
  同意はワークスペースごと。
- clientSecret は環境変数参照を推奨し、**いかなる環境固有値もリポジトリに
  コミットしない**（例示はプレースホルダのみ）。

### External Dependencies

- upstream: `https://mcp.slack.com/mcp`（SSE）
- Slack Web API: `files.getUploadURLExternal` / `files.completeUploadExternal` /
  OAuth `oauth/v2_user/authorize`・`oauth.v2.user.access`
- credential: 既存の自作 Slack App（scli と共有）の user token
- Go の外部ライブラリ依存: **ゼロ**（標準ライブラリのみ）

## 3. Design Decisions

- **言語**: Go。単一バイナリ・外部依存ゼロ・Developer ID 署名 + notarize という
  org の配布標準に合致。mcp-guardian で stdio⇔SSE プロキシと OAuth の実装実績あり。
- **mcp-guardian の骨格を参考に完全新造**: プロキシパイプ / SSE transport /
  authorization_code OAuth（トークン保存・refresh）/ tools/list マージ /
  tools/call ルーティング / JSON-RPC framing のみを参考とし、依存もコピーもしない。
  governance / classify / state / receipt / otlp / webhook / mask は持ち込まない。
  ガバナンスプロキシ（mcp-guardian）と機能拡張プロキシ（本ツール）の責務を混濁
  させないため。
- **swrite は非改変**: swrite は bot 名義の設計であり、user token を持ち込むのは
  筋違い（設計判断として棄却済み）。本ツールは純正コネクタと同じ**本人名義
  （user token）**で動作し、identity が一貫する。
- **単一トークン**: プロキシ中継とアップロードは同一の user token（同一 OAuth
  セッション）で行う。scli と共有する既存 App は user scope `files:write` を
  App レベルで許可済みのため、第 2 の credential は不要。
- **脅威モデル**: 本ツールは (1) 非信頼な Slack コンテンツを LLM へ中継し、
  (2) ローカルファイルを読み、(3) 外部（Slack）へ送信する — データ持ち出し
  （exfiltration）プリミティブである。したがって封じ込め（allowed_roots）は
  **operator が帯域外（config）でのみ規定**し、ツール引数や Slack 由来の値から
  決して導出しない。
- **明示的スコープ外**: 読み取り・通常投稿の再実装（純正コネクタと既存ツールが担う）
  / bot ワークフロー（swrite が担う）/ governance・telemetry / 1 プロセスでの
  マルチワークスペース多重化 / Streamable HTTP での待ち受け（Claude Desktop は
  stdio で足りる。必要になれば再検討）。

## 4. Development Plan

### Phase 1: Core

- JSON-RPC framing + stdio⇔SSE 透過プロキシ
- OAuth authorization_code フロー + トークン保存・refresh
- tools/list マージ + tools/call ルーティング（名前衝突検出含む）
- 注入ツール 2 本（external upload 3-step、root / thread）
- パス封じ込め（canonical 化 + allowed_roots 包含 + deny-by-default +
  通常ファイル限定 + 隠しパス成分拒否 + サイズ上限）
- テスト: **封じ込めの単体テストを最重要**（`..` 遡り / symlink 脱出 / 相対パス
  トリック / roots 未設定時 deny / 隠し成分の直接指定・symlink 解決経由・
  dot 配下 allowed_root の許容）。mock upstream + dummy MCP client harness で
  マージ・ルーティング・透過性を検証。

### Phase 2: Features

- ライフサイクル CLI（`init` の対話的 allowed_roots 登録・Claude Desktop 登録
  スニペット出力 / `login` / `config`）
- 最小監査ログ
- エラーテーブル整備（構造化エラーコードの一覧化）
- マルチワークスペース運用の使い勝手（config 複数持ちの導線）

### Phase 3: Release

- README.md / README.ja.md / CHANGELOG.md / AGENTS.md / LICENSE(MIT)
- 実ワークスペース E2E（root 添付・スレッド添付・path_denied・再認可導線）
- 署名 + notarize、org リリース手順（12 ステップ）
- chatops-series umbrella へ submodule 追加、org profile / web catalog /
  homebrew tap 更新、check-org.sh green

各 Phase は独立レビュー可能。Phase 1 は mock upstream で完結して検証できる。

## 5. Required API Scopes / Permissions

Slack user token scopes（既存 App = scli と共有。App レベルでは許可済み）:

- 既存の read/post 系: `chat:write`, `channels:history`, `channels:read`,
  `groups:history`, `groups:read`, `im:history`, `im:read`, `mpim:history`,
  `mpim:read`, `search:read`, `users:read`
- **追加**: `files:write`（アップロードに必須。OAuth 要求 scopes への追加と、
  ワークスペースごとに 1 回の再認可が必要）
- `files:read` は不要。

注意: 再認可で user token がローテートし得る。scli とトークンストアを別々に
保持している場合は、再認可後に scli 側の疎通確認（必要なら再ログイン）を行う。

## 6. Series Placement

Series: **chatops-series**

Reason: Slack ChatOps 自動化ツール群（swrite/scat/stail/slack-router/md-to-slack）
との同居が自然。シリーズの既存ツールは bot 認証だが、本ツールは純正コネクタと
identity を揃えるため **user token を用いる意図的な差異**であり、README に明記する。

## 7. External Platform Constraints

- 旧 `files.upload` API は廃止済み — external upload 3-step が必須。
- `completeUploadExternal` に `channel_id` を渡さないとファイルはどのメッセージにも
  紐付かず宙に浮く — channel を必須引数とすることで構造的に回避。
- Slack は**投稿済みメッセージへの後付け添付をサポートしない** — 添付は常に
  新規ルートメッセージまたはスレッド返信として投稿される。
- Slack user token はワークスペース単位 — ワークスペースごとの config + MCP 登録が
  必須（アーキテクチャ上の前提）。
- scope 追加はワークスペースごとの再認可を要求する。共有 App のためトークン
  ローテートが scli に波及し得る。
- Slack Web API のレート制限、および Slack 側のファイルサイズ上限に従う。
- `mcp.slack.com/mcp` は Slack 公式だが、その SSE エンドポイントの仕様・提供形態は
  変更され得る（本ツールの upstream 依存として最大のリスク要因）。

---

## Discussion Log

1. **発端**: 純正 Slack コネクタはファイル DL 可・添付不可。読み取り/通常投稿は
   既存実装が多数あるため、「添付投稿への特化」を方針とした。
2. **swrite 拡張案 → 棄却**: swrite に `mcp` サブコマンドを足す案を検討したが、
   (a) swrite は bot 専用設計で user token の持ち込みは筋違い、(b) upload は
   `completeUploadExternal` への `channel_id` 指定で投稿と一体化すべき、との
   指摘で棄却。swrite は非改変とする。
3. **独立 upload MCP 案 → 発展**: 本人名義（user token）の upload 専用 MCP を
   新設する案を経て、「純正 Slack MCP を透過しつつ upload だけ注入するプロキシ」
   構想へ発展。
4. **実現性の実証**: mcp-guardian で `mcp.slack.com/mcp`（SSE upstream）+
   Slack user OAuth（authorization_code）のプロキシが**動作済み**であることを確認。
   guardian が user token を自前保持し scopes も client 側指定であることから、
   `files:write` を要求 scopes に足すだけで**単一トークン**で閉じると判明。
5. **完全新造の決定**: mcp-guardian への相乗り（metatool 拡張）ではなく、
   guardian の骨格（プロキシ/OAuth/マージ/ルーティング）を参考にした専用ツール
   として新造。governance と機能拡張の責務混濁を避ける。独自 App の
   ワークスペースインストールは許容。
6. **credential 確定**: scli と共有する既存 App が user scope `files:write` を
   App レベルで許可済みと確認。OAuth 要求 scopes への追加 + ワークスペースごと
   再認可のみで単一トークン構成が成立。
7. **ファイル入力設計**: workspace_dir + 直接パスの両対応とし、当初は
   「config 固定の専用 workspace_dir を既定 allowed root とする」案だったが、
   **cowork ではエージェントが作業ディレクトリを握るため workspace_dir は
   ツール引数（エージェント指定）でなければ動作不能**との指摘で修正。
   封じ込めは operator 設定の allowed_roots に一本化し、既定は全面 deny +
   `init` での対話的登録とした。
8. **脅威モデル**: 非信頼 Slack コンテンツの中継 + ローカル読み取り + 外部送信 =
   exfiltration プリミティブと認定。allowed_roots は帯域外（config）でのみ規定し、
   ツール引数・Slack 由来値から導出しない、canonical 化 + 包含チェック、
   構造化 path_denied、最小監査ログを要件化。
9. **マルチワークスペース**: mcp-guardian と同様、ワークスペース単位の config +
   MCP 登録に分離（token がワークスペース単位のため。多重化はツール名衝突と
   透過性破壊を招くため不採用）。
10. **未決事項の確定**: 名前 = slack-mcp-extender / CLI = ライフサイクル最小限 /
    注入ツール = 2 本（root / thread 分離）/ allowed_roots 既定 = 全面 deny +
    init 登録。
11. **隠しパス成分拒否の追加**: exfil 標的（`.ssh`/`.aws`/`.env`/`.git` 等）が
    dot 配下に集中することから、allowed_roots 内側の defense-in-depth として
    dot 成分拒否を要件化。判定は allowed_root からの相対部分に限定（dot 配下の
    root 構成を壊さない）、`allow_hidden`（既定 false）で opt-out。
