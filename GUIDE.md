# GUIDE — このプロジェクトで Redis を学ぶ具体的手順

EXPERIMENTS.md が「記入用ワークシート（穴埋め）」なら、こちらは
「観察の道具立て＋各実験の詳細ランブック＋この環境で実測した本物の数字」。
数字はすべて手元で seed した実測値なので、あなたの環境でも近い値が出れば答え合わせになる。

> **起動の前提**：`docker compose up -d` で app(待機) と redis が上がる。
> API を叩きたい時だけ別端末で `docker compose exec app go run .` を起動する。
> 以下の実験で使う `seed` や `redis-cli` は HTTP サーバに依存しないので、
> up -d さえ済んでいれば動く。`is not running` が出たら `docker compose ps` /
> `docker compose logs app` を確認（README のトラブルシュート参照）。

---

## 0. まず「観察の道具」を手に入れる

実験は「動かす」より「測って観察する」が本体。使う道具はこれだけ。

### 0-1. キーの数え方は 2 種類ある（最初のつまずき）

このプロジェクトは全ユーザーを **1 つの Sorted Set** `leaderboard:global` に入れている。
だから「件数」を見る時に混同しやすい：

```bash
redis-cli DBSIZE                    # → 1     ... トップレベルの「キー」の数
redis-cli ZCARD leaderboard:global  # → 10000 ... ZSET 内の「メンバー」の数
redis-cli KEYS '*'                  # → leaderboard:global （キーは1本だけ）
```

**ユーザー数を見たいなら常に `ZCARD`。** DBSIZE は 1 のまま動かない。
この「キー vs 構造の中身」の区別が、実験2・3・5すべての伏線になる。

### 0-2. 観察コマンド早見表

| コマンド | 何が見えるか | どの実験で効く |
|------|------|------|
| `redis-cli INFO memory` | `used_memory_human`, `maxmemory` | 実験2 |
| `redis-cli INFO stats` | `evicted_keys`, `expired_keys` | 実験2 |
| `redis-cli INFO persistence` | `rdb_last_save_time`, `aof_enabled` | 実験1 |
| `redis-cli --latency` | 応答レイテンシの min/max/avg | 実験5 |
| `redis-cli --stat` | 1秒ごとの ops/秒・メモリ・接続数 | 全般 |
| `redis-cli SLOWLOG get N` | 遅かったコマンドと実行時間(µs) | 実験5 |
| `redis-cli MONITOR` | 全コマンドをリアルタイム表示（重いので観察時だけ） | 全般 |
| `redis-cli OBJECT ENCODING <key>` | 内部エンコーディング（後述） | 応用 |
| `redis-cli MEMORY USAGE <key>` | そのキーの実バイト数 | 実験2 |

### 0-3. 記録の作法

各実験ごとに「実測値をコミット」する。これが「言語化」の物証になる。

```bash
git init && git add -A && git commit -m "start"
# 実験1が終わったら
git add EXPERIMENTS.md && git commit -m "exp1: 永続化オフで全消失を確認 (ZCARD 10000→0)"
```

---

## 1. 実験1：速度 ⇔ 耐久性 【実測済み】

### 手順
```bash
docker compose up -d
docker compose exec app go run . -seed 10000
docker compose exec redis redis-cli ZCARD leaderboard:global
docker compose exec redis redis-cli INFO memory | grep used_memory_human
```

### この環境での実測
```
ZCARD leaderboard:global : 10000
used_memory_human        : 1.90M        # 1万メンバーで約1.9MB
DBSIZE                   : 1            # キーは1本
```

### わざと壊す
```bash
docker compose restart redis     # 永続化オフのまま再起動
docker compose exec redis redis-cli ZCARD leaderboard:global
```
実測：
```
ZCARD leaderboard:global : 0            # 全消失
```

### ④ なぜ（模範解答）
> RAM上のデータは、永続化を明示的に有効化しない限りプロセス終了で消える。
> Redis の既定は「速さ優先・揮発許容」であり、耐久性は足し算するオプション。
> だから Redis は真実の記録元（source of truth）ではなく、その手前の速い層に置く。

### ⑤ 直す — 3設定で「失う窓」を測る
`docker-compose.yml` の redis の `command:` を差し替えて、
seed → `docker compose kill redis`（graceful でない強制終了）→ `docker compose up -d`
→ `ZCARD` を測り、表を埋める：

| 設定 (`command:`) | kill後 ZCARD | 失った窓 | 備考 |
|------|------|------|------|
| `redis-server --save "" --appendonly no` | 0 | 全部 | 揮発 |
| `redis-server --save "60 1000"` (RDB) | ? | 直近スナップ以降 | fork+CoW で保存 |
| `redis-server --appendonly yes --appendfsync everysec` (AOF) | ? | 最大1秒 | 追記ログ |
| `... --appendfsync always` | ? | ほぼ0 | 毎回fsync=遅い |

**RDB は「点」で保存、AOF は「線」で追記**。everysec と always の書き込み速度差を
`redis-cli --stat` の ops/秒 で見ると、耐久性の代金がそのまま数字になる。

---

## 2. 実験2：メモリ ⇔ データ量 【実測済み・重要な発見あり】

`maxmemory` を小さく縛って、上限に当たった時の挙動をポリシー別に観察する。

### 2-A. noeviction（既定に近い・書き込みを拒否）
```bash
# docker-compose.yml の redis command を差し替え:
#   redis-server --save "" --appendonly no --maxmemory 3mb --maxmemory-policy noeviction
docker compose up -d
docker compose exec app go run . -seed 200000
```
実測：seeder が途中で落ちる。
```
2026/.. seeded 20000...
2026/.. OOM command not allowed when used memory > 'maxmemory'.
ZCARD             : 20521      # 上限で書き込みが止まった
used_memory_human : 3.04M
```
→ **上限に達したら新規書き込みを即エラーで弾く**。データは壊さないが、書けない。
→ 書き込みをエラーで弾く。

### 2-B. allkeys-lru（古いキーから追い出す）… ここで設計の落とし穴が出る
```bash
#   redis-server --save "" --appendonly no --maxmemory 3mb --maxmemory-policy allkeys-lru
docker compose up -d
docker compose exec app go run . -seed 200000
docker compose exec redis redis-cli ZCARD leaderboard:global
docker compose exec redis redis-cli INFO stats | grep evicted_keys
```
実測：seeder は最後まで走る（エラーなし）のに、
```
投入完了: 現在 17342 件     # 20万件入れたのに1.7万件しか残っていない
ZCARD        : 17342
evicted_keys : 9            # ★キーが9回まるごと追い出された
DBSIZE       : 1
```

### ★発見：単一キー設計では LRU が「全消し」になる
Redis のエビクションは **キー単位**。このプロジェクトは全員を1つの ZSET に
詰めているので、上限に当たると「一番冷たいメンバー」ではなく
**リーダーボードごと丸ごと蒸発**する。それが9回起きて、最後の残骸が17342件。

これは「メモリ⇔データ量」の教訓であると同時に、**キー設計が悪いとエビクションが
凶器になる**という実験3への強烈な伏線。分割していれば（例 `leaderboard:2026-08` の
ように時間で割る）、古いシャードだけが落ちて現行は生き残れた。

### ④ なぜ（模範解答）
> RAM は有限なので「全部載る」前提は置けない。上限到達時の振る舞い（拒否 or 追い出し）を
> ポリシーで選ぶ設計になっている。そして追い出しはキー単位なので、
> 巨大な単一キーは部分的に削れず、all-or-nothing の事故になる。
> ＝「何をホットとして残すか」はキーの切り方そのもの。

---

## 3. 実験3：低レイテンシ ⇔ リッチクエリ

### わざと壊す（できないことを体で知る）
「スコア100超の "日本の" ユーザーだけ上位10人」を出したくなる。だが ZSET 1本には
国の情報がない。無理に全走査すると：
```bash
docker compose exec redis redis-cli --eval /dev/stdin <<'LUA'
-- 全メンバーを舐めて国で絞る…ようなことは ZSET だけでは表現できない
return redis.call('ZCARD', 'leaderboard:global')
LUA
# 結局 ZRANGE 0 -1 で全件引いてアプリ側で絞る → 遅い（実験5で42msを実測）
```
→ 二次索引もJOINもないので、**「どう読むか」を先に決めてキーを設計**するしかない。

### ⑤ 直す — 読み方に合わせてキーを割る
`leaderboard.go` に国別キーへも書く経路を足す：
```go
// 国別リーダーボードにも同時に積む（読み方 = キーの形）
func (s *Service) AddScoreWithCountry(ctx context.Context, user, country string, pts float64) error {
    pipe := s.rdb.TxPipeline()
    pipe.ZIncrBy(ctx, key, pts, user)                       // 全体
    pipe.ZIncrBy(ctx, "leaderboard:"+country, pts, user)   // 国別
    _, err := pipe.Exec(ctx)
    return err
}
```
`TxPipeline` で2本の ZINCRBY をまとめて往復1回に。すると
「日本の上位10人」は `ZREVRANGE leaderboard:jp 0 9` で O(log N) 一発。
**クエリの自由度を捨てて、事前に決めたアクセスパターンへの速度を買う**のが Redis 流。

### ④ なぜ（模範解答）
> クエリプランナが無い＝実行時に最適経路を探してくれない。だから設計時に
> アクセスパターンを固定し、それ専用のキー構造を用意する。柔軟性を前払いで捨てて、
> 予測可能な O(1)/O(log N) を得るトレードオフ。

---

## 4. 実験4：可用性 ⇔ 強整合性（レプリケーション）

### 準備：replica を1台足す
`docker-compose.yml` に追記：
```yaml
  redis-replica:
    image: redis:7-alpine
    command: redis-server --replicaof redis 6379 --save "" --appendonly no
    depends_on: [redis]
```

### わざと壊す（ACK済み書き込みの喪失を再現）
```bash
docker compose up -d
# プライマリに書く
docker compose exec redis redis-cli ZADD leaderboard:global 999999 loser
# 伝播する前に一撃で殺す
docker compose kill redis
# replica に伝わっていたか？
docker compose exec redis-replica redis-cli ZSCORE leaderboard:global loser
```
非同期レプリのため、タイミングによっては replica に `loser` が無い＝
**クライアントには成功を返したのに消えた書き込み**が観察できる。
（確実に再現したければ、書き込み直後にマイクロ秒で kill する／`WAIT 1 100` を使わない）

### ④ なぜ（模範解答）
> レプリケーションは既定で非同期。書き込みのたびに全レプリカの確認を待てば遅くなるので、
> Redis は「待たずに返す＝性能・可用性優先」に倒している。フェイルオーバー時に直近の
> 未伝播分を失いうるのがその代金。強整合が要るなら `WAIT` や外部の記録元で補う。

---

## 5. 実験5：シングルスレッド ⇔ マルチコア 【実測済み】

### 軽いコマンド vs 重いコマンドの占有時間を測る
```bash
docker compose exec app go run . -seed 100000     # 10万件
# 全コマンドを SLOWLOG に記録させる
docker compose exec redis redis-cli config set slowlog-log-slower-than 0
docker compose exec redis redis-cli slowlog reset
docker compose exec redis redis-cli zrevrange leaderboard:global 0 9 withscores   # 軽い
docker compose exec redis redis-cli zrange   leaderboard:global 0 -1 withscores   # 重い
docker compose exec redis redis-cli slowlog get 2
```
この環境での実測（実行時間 = 単一スレッドを占有した時間）：
```
軽い ZREVRANGE(上位10) :     24 µs
重い ZRANGE(全10万件)  : 40763 µs  ≈ 41 ms   ← 約1700倍
```
アイドル時の `redis-cli --latency` は avg 0.06ms / max 1ms 程度。
つまり **重い1本が走る41msの間、他の全クライアントは待たされる**（head-of-line blocking）。

### 体感する
2つのターミナルで：
```bash
# 端末A: レイテンシを監視し続ける
docker compose exec redis redis-cli --latency
# 端末B: 重いコマンドを何度も投げる
docker compose exec redis redis-cli zrange leaderboard:global 0 -1 withscores >/dev/null
```
Bを叩いた瞬間、Aの max がスパイクする。Goのgoroutineでクライアントを増やしても
Redis側は1スレッドで直列化するので、詰まりは解消しない。

### ④ なぜ（模範解答）
> シングルスレッドはロック競合ゼロ・決定的で速い代わりに、1本の重いコマンドが
> 後続を全部止める。だから本番で `KEYS *` や巨大 `ZRANGE 0 -1` は禁忌で、
> `SCAN`／`ZSCAN` で分割し、範囲を区切って読む。スケールはインスタンス分割やClusterで。

---

## 6. 全部終えたら：核心の一文を書き直す

最初の会話で握った「Redis は遅くて壊れなくて高価なDBのクリティカルパスから、
ホットなデータを外へ逃がす層」を、**自分が採った実測値を添えて**書き直す。

> 例）「1万メンバーで1.9MB・上位10件24µs。ただし永続化オフなら再起動で全消失し、
> 　　メモリ上限では単一キーごと蒸発した。速さは本物だが揮発と粗いエビクションが代金で、
> 　　だから記録元にはせず、キーを read パターンに合わせて割って使う層だと理解した。」

この一段落が、当初の目的「高負荷設計の言語化」の完成物になる。

---

## 付録：さらに掘るなら

- `OBJECT ENCODING leaderboard:global` … 小さいZSETは `listpack`、大きくなると `skiplist`。
  seed 件数を増やしながらエンコーディングが切り替わる閾値を観察すると、メモリ最適化の思想が見える。
- `redis-cli --bigkeys` … どのキーが重いかを走査。単一キー設計の偏りが可視化される。
- `redis-cli MEMORY DOCTOR` / `LATENCY DOCTOR` … Redis自身の診断メッセージ。
- ここまで来たら、最初に話した「ミニRedis自作」へ。RESP→イベントループ→TTL→永続化の順。

---

## 発展A：大量の同時アクセスを捌く（飽和点とテールレイテンシ）【実測済み】

実験5は重いコマンドを1本ずつ手で投げた。ここでは Go の並行負荷クライアント `cmd/load` で
**同時アクセスを段階的に増やし**、単一ノードの限界を数字で見る。

```bash
# 10万件を積んでから。並行度を 1→512 に振り、各2秒測る
docker compose exec app go run ./cmd/load -op read -c 1,8,32,128,512 -d 3s
# 重いコマンド(ZRANGE 0 -1)を測定中に差し込む版（head-of-line blocking を混ぜる）
docker compose exec app go run ./cmd/load -op read -c 1,8,32,128,512 -d 3s -heavy
```

### この環境での実測（heavyなし）
```
並行度   rps      p50(ms)  p99(ms)  p999(ms)  max(ms)
1        53070    0.01     0.06     0.14      1.17
8        65811    0.11     0.56     1.05      4.14
32       56069    0.44     2.43     7.53      14.68
128      50823    2.58     7.01     14.14     70.92
512      51379    7.73     28.82    971.70    1894.18
```
読みどころ3点：
1. **飽和点は低い。** rps は並行度8あたり（約6.5万）で頭打ちになり、以降は増やしても伸びない。
   1コアだから、クライアントを増やしてもスループットは上がらない。
2. **p50 は並行度に比例して伸びる**（0.01→7.73ms）。捌ける速さは一定なのに待ち行列が伸びるから。
3. **テールは爆発する。** 並行度512で p999=971ms、max=1894ms。rps は横ばいなのにテールだけ悪化。
   ＝**飽和点を超えて同時接続を増やすと、スループットは増えずレイテンシ（特に裾野）だけ悪化する**。

### この環境での実測（heavyあり）
```
並行度   rps      p50(ms)  p99(ms)  p999(ms)  max(ms)
1        44572    0.01     0.06     0.16      63.40
8        56748    0.11     0.57     6.15      43.84
32       57150    0.42     1.93     28.09     34.98
128      42216    2.54     25.30    47.63     51.11
512      38238    10.69    78.39    352.04    363.98
```
最重要は**並行度1の行**。p50 は 0.01ms のまま（大半のリクエストは健全）なのに、
max が **63ms** に跳ねる。これは差し込んだ重い ZRANGE(約73ms)に light な読みが1本ぶつかった証拠。
**median は健全なのに tail だけ毒される**——head-of-line blocking のテール署名そのもの。
平均やp50だけ見ていると全く気づけない。

### 言語化（発展Aの核）
> シングルスレッドの Redis は、飽和点（この環境で並行度〜8）を超えると、
> 同時接続を増やしてもスループットは増えず、待ち行列が伸びてレイテンシ、特にテール
> (p99/p999)だけが悪化する。しかも重い1本が混じると、median が健全でも tail が跳ねる
> （並行度1でも max 63ms）。ゆえに高負荷設計では平均でなく p99/p999 を監視し、
> 重いコマンドを排除（SCAN化）し、飽和点の先はインスタンス分割/Cluster で
> 水平にスケールする——縦（1コア）には限界がある。

### 掘り下げアイデア
- `-op write` で書き込み負荷の飽和点も測り、read と比べる。
- `-c` をもっと細かく（1,2,4,8,16,...）振ると、rps が頭打ちになる正確な点が見える。
- 飽和点を「1コアの限界」と結論づけたら、次は方向B（レプリカで読み分散 / Cluster でシャーディング）へ。
  実験2・3で単一ノードで学んだ「キーの切り方」が、今度はノード分散の次元で再登場する。