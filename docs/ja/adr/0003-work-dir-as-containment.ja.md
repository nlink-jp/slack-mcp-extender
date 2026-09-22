# ADR-0003: 封じ込め境界は呼び出しごとの `work_dir`、`allowed_roots` は廃止

> Status: Accepted — 2026-09-13。拒む場所の判定は [ADR-0004](0004-pathguard.ja.md)（nlink-jp/pathguard）で置き換えた。[ADR-0002](0002-symmetric-download-with-write-containment.ja.md)（対称ダウンロードと書き込み封じ込め）を改定。

## Context

ADR-0001/0002 は、アップロードとダウンロードの双方を **operator 設定の
`allowed_roots`** に封じ込めると決めた。境界が要る判断（出る方向は exfiltration、
入る方向は書き込みプリミティブ）は正しく、そこは変えない。**変えるのは境界の出どころ**である。

`allowed_roots` は本来やりたかったことを表現できない機構だった —— 照合は解決後パスの
前方一致で、リポジトリ単位の粒度が無い。1 つの work root の下に 100 以上のリポがある
環境で共通に賄うには `~/works` か `~` を挙げるしかなく、`~` を開けた瞬間に `.ssh` も
`.aws` も通ってリストは意味を失う。運用者が実際に書けるのは狭い交換用ディレクトリの
列挙だけで、その結果「エージェントが今いるプロジェクトのファイルは送れない」になる。

欠けていたのは**ピンポイント指定**であり、組織 ADR-021 の `work_dir` がまさにそれである
—— 呼び出し側が呼び出しごとに 1 つ名指しする。

## Decision

1. **`workspace_dir` を `work_dir` に改名し、3 ツールすべてで必須にする。**
   意味は「呼び出し側が読み戻せる絶対パス」。
2. **`work_dir` が封じ込め境界そのもの。** `containment.Policy` は呼び出しごとに
   roots = [解決済み work_dir] で構築する。アップロードはその内側からのみ、
   ダウンロードはその内側にのみ着地する。`dest_dir` は省略時 `work_dir` 自身。
3. **`work_dir` は信頼する前に検証する** —— 絶対 / `~` 無し / `..` 無し / 存在する dir /
   書込可 / システム位置・ホームそのもの・資格情報ディレクトリ（`~/.ssh`、`~/.aws`、
   `~/.gnupg`、`~/.config/gcloud`、`~/Library/Keychains`、`~/.claude`、`~/.codex` 等）で
   ないこと。判定はパスの両方の綴り（渡されたまま／symlink 解決後）× 項目側の両方の
   綴りで行う。**ここは組織 ADR-021 §7 の唯一の例外である** —— 送るファイルは machine の
   外へ出るので、「呼び出し側が自分で読めたはず」は効く境界ではなく、`work_dir` 配下に
   限る。
4. **解決順は 引数 → `_meta["jp.nlink/work_dir"]` → エラー**（`work_dir_required`）。
5. **`allowed_roots` を config から削除する。** キーが残った config は起動時に
   名指しで落とす —— 運用者が書いたつもりの封じ込めリストが無言で無視されるのが、
   ここで起こりうる最悪の結果だからである。`init` の対話からも root の質問を外す。
6. ADR-0002 の他の判断（canonical 化の順序、隠し成分の拒否、通常ファイルのみ、
   サイズ上限、上書き禁止、監査ログ）はすべて維持する。判定の基準が
   「matched root」から「work_dir」に変わるだけである。

## Consequences

- **破壊的。** `workspace_dir` を送る呼び出しは拒否され、`allowed_roots` を持つ
  config は起動しない
- 交換用ディレクトリを事前に決めておく運用は不要になる。エージェントは自分が
  作業しているディレクトリからそのまま送れる
- 封じ込めの強さは変わらない —— むしろ狭くなる。以前は「operator が挙げた root の
  どれか」だったものが「この呼び出しが名指しした 1 つ」になる

## References

- 組織 ADR-021（work dir 契約。§7 のアップロード例外がこのサーバー）
- [ADR-0002](0002-symmetric-download-with-write-containment.ja.md)、
  [ADR-0001](0001-extension-tool-namespace.ja.md)
