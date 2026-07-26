# 実験ログ：Redis のトレードオフを手で確かめる（Go 版）

各実験は 5 ステップで記録する。**④の一文**が最終成果物。

1. 仮説（何が起きると思うか）
2. ナイーブに動かす（手順・数値）
3. わざと壊す（観察した壊れ方・数値）
4. なぜ？ → 設計思想を**一文**で
5. 直す（対策と、直後の数値）

seeder は `docker compose exec app go run . -seed <件数>` で叩く。

---

## 実験1：速度 ⇔ 耐久性（← まずここから）

**軸**：RAM は速いが揮発する。どこまで失うことを許容するか。

### ① 仮説
- 永続化オフだと、プロセス再起動で全データが消える？

### ② ナイーブに動かす
初期状態の `docker-compose.yml` は `--save "" --appendonly no`（永続化オフ）。

```bash
docker compose up -d
docker compose exec app go run . -seed 10000
docker compose exec redis redis-cli DBSIZE      # → 10000 のはず
```

### ③ わざと壊す
Redis だけ再起動して、中身を確認する。

```bash
docker compose restart redis
docker compose exec redis redis-cli DBSIZE      # → ?
```

観察を記録：__________（例：0 になった＝全消失）

### ④ なぜ？（一文で言語化）
> 記入：__________
> （ヒント：RAM は電源が切れれば消える。だから Redis は既定で「速さ」を取り、
> 　永続化は明示的に足すオプション。＝真実の記録元にはしない設計）

### ⑤ 直す ＆ 窓を測る
`docker-compose.yml` の redis command を切り替えて比較する。

- **RDB（スナップショット）**：`redis-server --save "60 1000"`
  → 「60秒ごと（1000変更以上あれば）に丸ごと保存」。クラッシュ時は直近スナップ以降を失う。
- **AOF（追記ログ）**：`redis-server --appendonly yes --appendfsync everysec`
  → 毎秒 fsync。失う窓は最大1秒だが書き込みコストが乗る。

各設定で seed → `docker compose kill redis` → `up` → `DBSIZE` を測り、
「どれだけ残ったか（=失った窓）」を記録：

| 設定 | kill 後に残った件数 | 失った窓 | 体感の書き込み速度 |
|------|------|------|------|
| 永続化オフ | | | |
| RDB save 60 1000 | | | |
| AOF everysec | | | |

**この表が「なぜ Redis を source of truth にしないか」の証拠になる。**

---

## 実験2：メモリ ⇔ データ量

**軸**：RAM は高価で有限。全データは載らない。

### ② ナイーブ
`maxmemory` を小さく設定（例 `--maxmemory 20mb --maxmemory-policy noeviction`）して
`go run . -seed 500000` を流す。

### ③ 壊す
- `noeviction` だと：新規書き込みが `OOM command not allowed` で弾かれるのを観察。
- `--maxmemory-policy allkeys-lru` に変えると：古いキーが黙って消えるのを観察。

`redis-cli INFO memory` の `used_memory_human` と `evicted_keys` を記録。

### ④ なぜ？（一文）
> 記入：__________
> （ヒント：「ホットな一部だけ載せる」は制約ではなく前提。だからエビクション方針が存在する）

---

## 実験3：低レイテンシ ⇔ リッチクエリ

**軸**：クエリプランナも二次索引もない。アクセスパターン先行でキーを設計する。

### ③ 壊す
「スコア100超のユーザーを国別に」みたいな条件を出そうとして、ZSET 1本では表現できないことに気づく。
`KEYS *` や全 `SCAN` で無理やり走査 → 遅い＆重いのを `redis-cli --latency` で観察。

### ④ なぜ？（一文）
> 記入：__________
> （ヒント：先に「どう読むか」を決めてキーを設計する。例：`leaderboard:jp` を別 ZSET で持つ）

### ⑤ 直す
Go 側で国別キーに二重に `ZADD`（`AddScore` を国別キーにも打つ）など、
データの持ち方で解く設計に。`leaderboard.go` に `AddScoreTo(ctx, key, ...)` を足すのが素直。

---

## 実験4：可用性 ⇔ 強整合性（レプリケーション）

**軸**：非同期レプリのため、フェイルオーバー時に ACK 済み書き込みを失いうる。

### 準備
`docker-compose.yml` に replica を1台追加：

```yaml
  redis-replica:
    image: redis:7-alpine
    command: redis-server --replicaof redis 6379
    depends_on: [redis]
```

### ③ 壊す
プライマリに書き込み → 即 `docker compose kill redis` → replica を昇格させ、
直前の書き込みが replica に伝播していない（消えている）ケースを再現。

### ④ なぜ？（一文）
> 記入：__________
> （ヒント：整合性より可用性・性能に倒す判断。CAP 的な割り切り）

---

## 実験5：シングルスレッド ⇔ マルチコア

**軸**：ロック競合ゼロで単純・速いが、1コアで捌く。重いコマンド1本で全員が詰まる。

### ③ 壊す
別ターミナルで `redis-cli --latency` を回しながら、
巨大 ZSET に対して `ZRANGE leaderboard:global 0 -1 WITHSCORES` や `KEYS *` を1本投げる。
その瞬間 latency が跳ねる（head-of-line blocking）のを観察。

Go 側から負荷をかけるなら、goroutine を多数立てて同時に `POST /leaderboard/score` を叩き、
その最中に重いコマンドを差し込むと、クライアント並行性を上げても Redis 側が
1スレッドで直列化する様子が見える。

### ④ なぜ？（一文）
> 記入：__________
> （ヒント：1本の重いコマンドが後続を全部止める。だから本番で KEYS は禁忌、SCAN を使う）

---

## まとめ（全実験を終えてから書く）

Redis の核心を、実験で得た数値を添えて自分の言葉で1段落に：

> 記入：__________
