# ADR-0004: パスの判定は nlink-jp/pathguard に任せる — upload はマシンの外へ出るものとして判定する

> Status: Accepted — 2026-09-22. Amends [ADR-0003](0003-work-dir-as-containment.ja.md)（work_dir の検証と資格情報の床）。

## 背景

ADR-0003 以来、`work_dir` の検証と、ファイルに当てる資格情報の床（`workdir.DeniedPath`）は
`internal/workdir` にあった。voice-scribe（組織 ADR-021 の参照実装）の写しからさらに離れた形で、`.env` の規則が
無く、検査の順も独自で、場所を**名前で**比べていた。APFS は既定で大文字小文字を区別しないので、同じ場所が別の
綴りで検査を通った。ホームディレクトリが分からないときは、資格情報の検査がすべてを通した。

組織はこの判定を 1 つのモジュールにまとめた（`nlink-jp/pathguard`、lib-series）。場所をファイルの実体と、ディスクと
同じやり方で同一視した名前で比べ、一覧は gem-agent・lagent と同じものを 1 つ持ち、読み書き（Local）とマシンの外へ
出るもの（Outbound）とで方針を分ける。

## 判断

- `github.com/nlink-jp/pathguard` v0.2.0 を依存に加える。CLAUDE.md の依存の規則は「サードパーティのモジュールを
  持たない。この組織のモジュール（同じ規則を守るもの）は可」と言い換える（運用者の判断、2026-09-22）。
- `internal/workdir` はアダプタにする。呼び出しの形（`Resolve(arg, meta, serverDirs)`・`Validate(dir, serverDirs)`）は
  そのままで、`Error` とコードは pathguard のものを出し直す（`work_dir_denied` は `details` に `reason` を持つ）。
- ファイルの床は向きで分ける: `UploadDenied`（Outbound。upload はマシンの外へ出るので、資格情報の名前はどこに
  あっても拒む）と `DownloadDenied`（Local）。`containment.Policy` は upload の元（`Resolve`）に前者を、download の
  保存先（`ResolveNewFile`）に後者を当てる。どちらも名指しされたままのパスと解決したパスの両方で（pathguard が
  途中のリンクをすべてたどる）、解決できないときは名指しされたパスだけで判定するので、存在しない資格情報
  ファイルは `not_found` ではなく拒否になる。
- ファイルの方針はシステムの場所を設計上含まない。システムのツリーの上にある作業ディレクトリは、root で動く
  サーバーなら届く（`work_dir=/private` は書き込めないことでしか止まらない）。`DirDenied` が pathguard の
  `CheckBeneath`（作業ディレクトリにしてはならない場所の一覧）を、ファイルのあるディレクトリに両方向で当てる。
- 床での拒否は `path_denied` の `reason: sensitive_path`（呼び出し側が分岐に使う値）のままとし、pathguard の理由を
  `details.floor_reason` に加える —— `work_dir_denied` が持つのと同じ語彙。
- `config.Config.ServerOwnedDirs` は各ディレクトリを絶対パスにする。pathguard は絶対パスでない場所があると
  すべての呼び出しを拒むので、相対の `state_dir` や `$HOME` ではそうなっていた。
- `work_dir_denied` の `details` を呼び出し側に返す（これまでは捨てていた）。

## 結果

- **upload で新たに拒む**: 秘密の名前を持つファイル（`id_rsa`、`credentials.json`、`*service-account*.json`）と、
  どこにあってもパスが資格情報のディレクトリ名・ファイル名（`.ssh`、`.aws`、`.npmrc`、`.netrc`、
  `.git-credentials`、`.bash_history`、`.docker/config.json` など）を通るファイル（`allow_hidden` のときの
  プロジェクトの `.npmrc` も）。ランタイムと同じ一覧のうちホームにある本物の場所
  （新たに `~/.kube`、`~/.config/gh`、`~/.netrc` など）。あらゆる綴り、直下のリンクの指す先。
- **`.env` を新たに拒む**（`sensitive_path`）。これまでは `allow_hidden=true` のとき upload できた。封じ込めの
  テスト 3 本は `.env` を隠しファイルの例にしていたので、ふつうの隠しファイルの例に改め、`.env` が
  `sensitive_path` で拒まれることを別に確かめる。ひな形（`.env.example` など）は通す。
- **ホームが分からなければすべて拒む**。`$HOME` がアカウントのホームと違うときは、資格情報・エージェント制御の
  場所を両方のホームで守る。このサーバー自身のディレクトリはこれまでどおり `$HOME` に従う。
- **両方向で新たに拒む**: ディレクトリがシステムの場所であるファイル。解決できないパス（終わらないリンクの
  連鎖、NUL、4096 バイト超）を `unresolvable_path` で。上にある `work_dir` 経由でホーム直下のファイルを
  名指したもの（`home_dir`）。Linux でも `/etc` を作業ディレクトリとして拒む。
- 写しを持たないので、判定の修正は pathguard のリリースと、ここでの依存の更新 1 行になる。

## 参照

- 組織 ADR-021（ファイル渡し MCP サーバーの work dir 契約）
- ADR-0003（work_dir を封じ込めの境界にする）
- nlink-jp/pathguard の RFP（`docs/ja/pathguard-rfp.ja.md`）
