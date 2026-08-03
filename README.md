# leaderboard-lab (Go 版)

Redis のトレードオフを「作って、わざと壊す」で体得するための学習用リポジトリ。
題材はランキング（リーダーボード）。Go(標準net/http) + go-redis(v9) + Docker。

## 構成

```
leaderboard-lab-go/
├── docker-compose.yml            # app(Go) + redis。redis は最初「永続化オフ」
├── go.mod / go.sum
├── main.go                       # HTTP サーバ + ルーティング + -seed フラグ
├── internal/
│   └── leaderboard/
│       └── leaderboard.go        # 中核サービス（ZSET のロジック）
├── EXPERIMENTS.md                # ★言語化の本体。実験の記録テンプレ
└── README.md
```

PHP(Laravel) 版との対応：`LeaderboardService.php` → `internal/leaderboard/leaderboard.go`、
artisan コマンドの seeder → `main.go` の `-seed` フラグ、コントローラ → `main.go` のハンドラ。

## セットアップ

依存の解決とビルドはコンテナ内で走るので、ローカルに Go は不要。

```bash
# 1) コンテナ起動。app は sleep infinity で待機状態になる（HTTPサーバはまだ立たない）
docker compose up -d

# 2) API サーバを起動（ログが見えるようフォアグラウンド。この端末は開けたままにする）
#    初回は go run が依存取得＋コンパイルするので数秒〜十数秒かかる
docker compose exec app go run .
```

確認（別ターミナルで）：

```bash
curl -X POST localhost:8000/leaderboard/score -d "user=alice&points=120"
curl -X POST localhost:8000/leaderboard/score -d "user=bob&points=90"
curl -X POST localhost:8000/leaderboard/score -d "user=alice&points=50"   # 原子加算で170に
curl localhost:8000/leaderboard
```

大量投入（負荷・メモリ実験用）— さらに別ターミナルで。**seeder は HTTP サーバとは独立**なので、
サーバを起動していなくても（手順2を飛ばしても）Redis さえ上がっていれば実行できる：

```bash
docker compose exec app go run . -seed 100000
```

> なぜ `up` でサーバを自動起動しないのか：`command: go run .` にすると、
> コンパイルや依存取得の失敗でコンテナごと落ち、以降の `exec` が
> `service "app" is not running` になる。`sleep infinity` で常時生存させ、
> サーバも seeder も `exec` で起動する形にして、この事故を避けている。

Redis を直接覗く：

```bash
docker compose exec redis redis-cli
> ZREVRANGE leaderboard:global 0 9 WITHSCORES
> INFO memory
> DBSIZE
```

## API

| メソッド | パス | 説明 |
|------|------|------|
| GET  | `/leaderboard` | 件数 + 上位10件 |
| POST | `/leaderboard/score` | `user`, `points`(数値) を加算。原子操作(ZINCRBY) |

## 進め方

`EXPERIMENTS.md` を上から順に。各実験は
「①ナイーブに動かす → ②わざと壊す → ③数値と壊れ方を記録 → ④なぜかを一文で言語化 → ⑤直す」
の順。この記録そのものが「高負荷設計の言語化」の成果物になる。

コードを編集したら、サーバを動かしている端末で `Ctrl-C` → もう一度
`docker compose exec app go run .`（go run が再コンパイルするので反映は自動）。
コンテナ自体は sleep infinity で生き続けるので落ちない。

## トラブルシュート

`docker compose exec app ...` が `service "app" is not running` になる場合：

```bash
docker compose ps        # app の STATUS を確認（Up でなければ落ちている）
docker compose logs app  # 落ちた理由を確認
docker compose up -d      # 上げ直す
```

`up -d` は「起動を試みた」時点で成功を返すため、app が直後に落ちていても
成功に見える点に注意。原因は必ず `logs app` に出る。
