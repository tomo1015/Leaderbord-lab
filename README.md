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
# 起動（初回は go run が依存を取得してから立ち上がる）
docker compose up
```

確認：

```bash
curl -X POST localhost:8000/leaderboard/score -d "user=alice&points=120"
curl -X POST localhost:8000/leaderboard/score -d "user=bob&points=90"
curl -X POST localhost:8000/leaderboard/score -d "user=alice&points=50"   # 原子加算で170に
curl localhost:8000/leaderboard
```

大量投入（負荷・メモリ実験用）— 別ターミナルで：

```bash
docker compose exec app go run . -seed 100000
```

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

コードを編集したら `Ctrl-C` → `docker compose up` で反映（go run のため再ビルドは自動）。
