# ADR-0005: 新旧両方の時代をそのまま通す — 仕様が HTTP のクライアントに求める分を nlink-jp/mcp で足し、追加ツールの答えを時代に合わせる

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-11-27 |
| Binds | slack-mcp-extender |
| Decision makers | nlink-jp maintainers |
| Triggered by | 組織 ADR-030（MCP 2026-07-28 への両時代対応。移行の順は中継が最初。§1・§2 は 2026-11-27 にこの設計に合わせて改められた）と、共通モジュール nlink-jp/mcp v0.1.0 の公開 |

## Context

### いまの slack-mcp-extender

公式の Slack MCP（`mcp.slack.com/mcp`、Streamable HTTP）を stdio のエージェントへ透過に中継し、継ぎ目は 2 つだけ
である。`tools/list` の結果に追加ツール（`ext_*`）の定義を足すことと、追加ツールの `tools/call` を手元で答える
ことである（`internal/proxy`、CLAUDE.md「Transparency」）。上流への POST に付けるのは `Content-Type`・`Accept`・
`Mcp-Session-Id`（上流が発行したとき）・`Authorization` だけである（`internal/transport/sse.go`）。

### 何が足りないか（仕様と実測・コード）

1. **旧方式でも、必須のヘッダを送っていない。** 2025-06-18 から、HTTP のクライアントは交渉した版を
   `MCP-Protocol-Version` に載せなければならない。いま通っているのは、サーバーが「ヘッダの無い要求を
   2025-03-26 として扱ってよい（MAY）」からである（コードで確認）。
2. **新方式の要求に、必須のヘッダを付けられない。** 本文から作る `MCP-Protocol-Version`・`Mcp-Method`・
   `Mcp-Name` と、`x-mcp-header` の印の付いた引数の `Mcp-Param-*` は、2026-07-28 の HTTP のクライアントの
   MUST である。印の不正なツールを除くのも MUST である。
3. **手元の答えが時代に合っていない。** 新方式の要求への結果には `resultType` が要り、`_meta` に
   `serverInfo` を載せるべき（SHOULD）である。追加ツールの結果はいま、どちらも持たない（コードで確認）。
4. **長すぎる行 1 つで止まる。** エージェント側の行を `bufio.Scanner`（上限 1 MiB）で読み、上限を超えると
   読み取りを抜けて終了する。上流の SSE も 1 行 1 MiB の `bufio.Scanner` で読み、超えるとそのストリームの
   読み取りが黙って終わる。その要求は 120 秒のタイムアウトまで待つ（コードで確認）。
5. **上流の JSON の本文を上限なしで読む。** `application/json` の応答を `io.ReadAll` で読んでいる（コードで確認）。
6. **2xx でない応答の扱いが欠けている。** `application/json` の本文は、状態を問わず、JSON-RPC であるかも
   確かめずに届ける。JSON-RPC のエラー本文の `id` が `null` だと、待っている要求と結び付かない。proxy は
   要求を 1 つずつ扱い、転送した要求の答えを待つ間は次のエージェントの行を読まないので、エージェントの
   入力全体が 120 秒のタイムアウトまで止まる。JSON でない 4xx は送信の失敗になり `-32603` で答える
   （コードで確認）。
7. **エージェントの壊れた JSON に答えない。** 読めない行はログに出して捨てる。要求だったなら、エージェントは
   答えを待ったままになる（コードで確認）。

相手（2026-11-27 に版も含めて測り直した。記録あり）: Slack の公式 MCP（Slack MCP 1.0.0）は旧方式のみ
（`server/discover` に `-32601`、`initialize` に 2025-06-18）。Claude Code 2.1.311 は両対応。

## Decision

§1・§2・§3 のうち上流の扱い・§5〜§8 は、mcp-bridge ADR-0008 と同じ決定であり、同じ言葉で書く（二つの中継が
同じ形を持つため）。違いは、追加ツール（§3 の最初の項・§4）と、上限の値（エージェントの行と SSE は 1 MiB の
まま）である。

### 1. 依存

require に `github.com/nlink-jp/mcp` を足す。CLAUDE.md の規則（標準ライブラリと、同じ規則を守る nlink-jp の
モジュールだけ）の内側である。

### 2. 時代は変換せず、そのまま通す

エージェントが開いた時代のまま上流へ通す。slack-mcp-extender は `initialize` にも `server/discover` にも
自分では答えない（ADR-030 §1 の改めた文面）。中継はエージェントが両対応なら両対応である。旧方式しか話さない
エージェントが新方式専用の上流につながるのは、そのエージェントの移行の後である。

### 3. 本文を変えるのは、次の 4 つだけ

- **追加ツールの定義を `tools/list` の結果に足す**（いまと同じ）。上流の結果の他のキー（新方式の `resultType`・
  `ttlMs`・`cacheScope`・`nextCursor` を含む）は残す。
- **新方式の `tools/list` の結果から、`x-mcp-header` の印が不正なツールを除く**（仕様の MUST、
  `httpwire.Tracker.Incoming`。5000 を超えた分も除く）。除いたツールの名前と理由をログに出す。除くのは、
  追加ツールを足す前である。
- **上流が JSON-RPC として答えなかった失敗には、`-32603` を作って返す。** 2xx でない JSON-RPC のエラー本文の
  `id` が欠けている・`null`・違うときは、要求の `id` に直す（`httpwire.Classify`。Context 6 を直す）。
  2xx の `application/json` の本文が JSON として読めないときも、要求の `id` で `-32603` を作る。
- **大きすぎる応答には `-32603` を作って返す**（§6。Context 4・5 を直す）。

それ以外の本文は、中身を変えずに通す（1 行に詰める圧縮は `wire.Writer` が行う）。

### 4. 追加ツールの答えを時代に合わせる

追加ツールの `tools/call` は `server.Inspect` で読み、結果を `server.ShapeResult` で形にする。新方式の要求なら
`resultType: "complete"` と `_meta["io.modelcontextprotocol/serverInfo"]`（slack-mcp-extender の名前と版）を
足す。旧方式ならいまのまま返す（Context 3 を直す）。

手元で答える要求の版は、拡張では検査しない。版の検査を書けば、`Frontend` が持つ検査の写しになる
（ADR-030 §2）。`Frontend` を使うには、`initialize` を含む全ての行をそこに通す必要があり、`initialize` に
答えない中継（§2）とは合わない。版の誤ったエージェントは、同じ接続の上流への要求（`tools/list` など）で
上流に断られる。誰が何を新しく得るかで言えば、利用者自身のエージェントが、版を偽って自分のツールを
呼べるだけであり、新しく得るものは無い。

### 5. ヘッダは本文から作る

上流への POST ごとに `httpwire.Tracker.Outgoing` のヘッダを付ける。

- 旧方式のメッセージ: `MCP-Protocol-Version` に `initialize` で決まった版（Context 1 を直す）。`initialize`
  そのものには付けない。
- 新方式のメッセージ: `MCP-Protocol-Version`・`Mcp-Method`・`Mcp-Name`・`Mcp-Param-*`（値が無い・null なら
  付けない。素の値で表せなければ Base64 の哨兵）。

`Outgoing` がエラーを返したとき:

- **バッチ（配列の本文）**: プロトコルのヘッダを付けずに、いまと同じく送る。ログに記録する。
- **印の付いた引数をヘッダに写せない**、または **`params.name` の無い `tools/call`**: 上流へ送らず、その要求に
  `-32602` で答える（値はメッセージに載せない）。

### 6. 応答は、届ける前にすべて Tracker を通す

- 2xx の JSON の本文と SSE の各 `message` イベントの本文（バッチを開いた後の各メッセージ）を
  `Tracker.Incoming` に通してから届ける。
- 2xx でない応答は `Classify` で判定する（Deliver・Synthesized・Drop）。401 のやり直しはいまのまま、その前に
  行う。
- **上限**: 2xx の JSON の本文は 8 MiB まで。超えたら、その要求の `id` で `-32603`「upstream response larger
  than 8388608 bytes」を返す（Context 5 を直す）。SSE は 1 行 1 MiB（いまと同じ）に加えて、1 イベントの
  データ行をつないだ大きさも 1 MiB までとする。どちらかを超えたらそのストリームの読み取りをやめ、そのストリームを
  開いた要求にまだ答えが届いていなければ、その `id` で `-32603` を返す（Context 4 を直す）。

### 7. HeaderMismatch は、転送層の中で 1 回だけやり直す

**対象**: 送った要求が `tools/call` で、`Outgoing` が `Mcp-Method` を付けた（新方式の）ものであり、その答えが
HTTP 400 で、本文の JSON-RPC の `error.code` が `-32020` のとき。判定は自分の要求と答えの本文を読んで行う。

1. 転送層が自分で `tools/list` を上流へ送る。`id` は `"slack-mcp-extender-refresh-<連番>"`（文字列）。
   `_meta` は元の要求の版・`clientCapabilities`・`clientInfo` を写す。`nextCursor` を最大 10 ページまで
   たどる。応答は `Tracker.Incoming` に覚えさせるだけで、エージェントには届けない。
2. 元の要求を、作り直したヘッダで 1 回だけ送り直す。その答えを、そのまま届ける。
3. 取り直しが失敗したら、送り直さずに最初の `-32020` を届ける。最初のページの後に失敗した場合、覚える印は
   読めたページの分になる。

やり直しは要求ごとに 1 回、取り直しは直列にする。拡張が自分で要求を作るのは、これだけである。

### 8. エージェント側の行は nlink-jp/mcp/wire で読み書きする

上限はいまの 1 MiB を保つ。長すぎる行は読み捨てて続け、`-32700`（`id: null`、「message longer than 1048576
bytes」）を返してログに出す（Context 4 を直す）。壊れた JSON にも `-32700`（`id: null`）を返す（Context 7 を
直す。mcp-bridge と同じ）。書き込みは `wire.Writer`。

### 9. 変えないもの

単一のユーザートークン、追加ツールの名前空間（ADR-0001）、work_dir による封じ込め（ADR-0003・0004）、
セッションの扱い（上流が発行したときだけ）、要求ごとのタイムアウト（120 秒）、要求を 1 つずつ扱う proxy の作り。

### 10. リリース前の検証で改めたこと（2026-11-27）

リリース前の独立検証の指摘のうち、設計が定めていなかった次の 4 点を、運用者の判断で決めた。§3〜§9 のこれらの
範囲は、この節が優先する。

1. **答えられないまま残る要求にも `-32603` で答える**（§3・§6 に足す）。要求が開いた SSE が、その要求の答えを
   送らずに終わったとき（JSON でないデータのイベントは捨てて記録する）と、要求への 2xx が空のとき（202 を
   含む）、その要求の `id` で `-32603` を返す。通知への 202・空の 2xx は、いまと同じく何も返さない。
2. **ヘッダを作れない要求の、残りの場合**（§5 に足す）。`params.name` の無い `prompts/get` と `params.uri` の
   無い `resources/read` は、`tools/call` と同じく送らずに `-32602` で答える。旧方式のメッセージで、
   `initialize` で決まった版がヘッダに載せられない文字を含むときは、プロトコルのヘッダを付けずに送って記録する
   （移行前と同じ動き。その版の誤りでセッション全体を使えなくしない）。新方式の要求で、版か method がヘッダに
   載せられないときは `-32602`。
3. **セッションを終える DELETE**（§9 に足す）。上流が発行したセッションを終える DELETE にも、`initialize` で
   決まった版の `MCP-Protocol-Version` を付ける（2025-06-18 から、HTTP のクライアントは後に続くすべての要求に
   このヘッダを付ける）。
4. **取り直しの期限**（§7 に足す）。1 回の取り直し（最大 10 ページ）の全体を 60 秒で打ち切り、過ぎたら取り直しの
   失敗として扱う（1 ページずつの期限では、最悪 10 分エージェントの要求が待つ）。

## 詳細設計

| 部品 | 変えること | 判定役 |
|---|---|---|
| `internal/proxy`（`proxy.go`） | エージェント側の行を `wire.Reader`（1 MiB）・`wire.Writer` に。長すぎる行と壊れた JSON に `-32700` | wire の約束（モジュール詳細設計 §3.1）と §8 |
| `internal/proxy`（追加ツールの答え） | `server.Inspect` と `server.ShapeResult` | 仕様 basic §Responses（`resultType`）、§4 |
| `internal/transport`（`sse.go`） | `Tracker` を持つ。`doPost` に `Outgoing` のヘッダと §5 のエラーの扱い。応答を `Classify`・`Incoming` に。§3・§6 の作る `-32603` と上限。§7 のやり直し | 仕様 streamable-http の各節 |
| `internal/jsonrpc` | `MergeToolsListResult` が上流の他のキーを残すことを確かめる（変えない見込み） | §3 |

## テスト設計

- **単体（transport）**: 旧方式の `initialize` の後に `MCP-Protocol-Version` が付く。新方式の `tools/call` に
  標準ヘッダと印の `Mcp-Param-*` が付く。JSON と SSE の両方で印を覚える。印の不正なツールが除かれる。
  `id: null` の 400 が要求の `id` で届く（タイムアウトを待たない）。HTML の 502・2xx の壊れた JSON が `-32603`
  になる。8 MiB を超える JSON の本文、1 MiB を超える SSE の行とイベントに、要求の `id` で `-32603` が返る。
  バッチがヘッダなしで送られる。写せない引数と名前の無い `tools/call` が送られずに `-32602` になる。
- **単体（やり直し）**: mcp-bridge ADR-0008 と同じ場合（成功・2 回目の失敗・取り直しの失敗・届かない取り直しの
  応答・1 回だけ・対象外の要求とコード）。`-race`。
- **単体（proxy）**: 新方式の追加ツールの答えが `resultType` と `serverInfo` を持ち、旧方式の答えは変わらない。
  新方式の `tools/list` のマージが `resultType`・`ttlMs`・`cacheScope` を残す。長すぎる行と壊れた JSON の後も
  中継が続き、`-32700` を返す。既存の封じ込めのテストはすべてそのまま通る。
- **探りの組み合わせ**: mcp-bridge ADR-0008 と同じ（上流 3 種 × 探りのクライアント 3 種、期待値は仕様の
  互換性表。HTTP の上流の探りはこのリポのテストに置く）。追加ツールの定義が、どの組み合わせでも
  `tools/list` に現れる。
- **実機（E2E）**: Claude Code 2.1.311 を拡張越しに Slack へ、行を記録しながらつなぐ。`server/discover` に
  `-32601` が返って旧方式へ戻る。上流への POST に `MCP-Protocol-Version: 2025-06-18` が付く。`tools/list` に
  公式ツールと `ext_*` が並ぶ。読むだけの公式ツールを 1 回呼べる（`slack_read_user_profile`、自分）。
  手元の答えの経路は、`ext_file_download` を存在しない `work_dir` で呼び、Slack に触れずに構造化エラーが
  返ることで確かめる（アップロードはしない）。新方式の手元の答えは、上流が旧方式なので実機では通らない。
  探りの組み合わせで確かめる。

## 開発計画

1. この ADR の承認（日英）。
2. 依存と行の読み書き（§1・§8）。
3. ヘッダと応答（§3・§5・§6）。
4. 追加ツールの答え（§4）。
5. やり直し（§7）。
6. 文書（README 日英・CLAUDE.md の透明性の項・AGENTS.md・CHANGELOG）。
7. 実機の E2E とリリース前の独立検証を 1 回（この ADR に照らして）。
8. リリース（minor: 0.6.0）。mcp-bridge のリリースの後に行う。

## Consequences

- Slack が新方式に移っても、両対応のエージェントはそのままつながる。旧方式でも、仕様どおりのヘッダで話す。
  上流の大きな応答や `id: null` のエラーで、エージェントの入力が止まることが無くなる。
- 透明性の規則は「本文は通す。足すのは追加ツールの定義、除くのは仕様が除けと言うツールだけ」になる。
  CLAUDE.md を改める。

## Alternatives considered

| 案 | 採らない理由 |
|---|---|
| 時代を変換する | mcp-bridge ADR-0008 と同じ理由。拡張は透過の中継であることを設計の約束にしている |
| 追加ツールの答えを旧方式の形のままにする | 新方式のクライアントは `resultType` の無い結果を `complete` と読むが、それは旧方式のサーバーのための規則である。中継の先の上流が新方式なら、同じ接続で形が混ざる |
| 手元で答える要求の版を拡張で検査する | `Frontend` の検査の写しになる。`Frontend` を使うには `initialize` に答える作りが要る。検査しないことで誰かが新しく得るものは無い（§4） |
| HeaderMismatch をそのまま返す | エージェントはヘッダを直せない |

## References

- 組織 ADR-030（§1・§2 は 2026-11-27 に改められた）、mcp-bridge ADR-0008
- nlink-jp/mcp v0.1.0 参照マニュアル（`httpwire`・`wire`・`server`）
- MCP 2026-07-28 `basic/transports/streamable-http`、`basic/index` §Responses
- ADR-0001（追加ツールの名前空間）、ADR-0003・0004（封じ込め）
