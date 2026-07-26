// Package leaderboard はリーダーボードの中核。
// Redis の Sorted Set（ZSET）1本で「順位つきランキング」を O(log N) で実現する。
// ここが「アプリ側で書くはずのロジックを、サーバ側の原子操作に落とす」の実例。
package leaderboard

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

const key = "leaderboard:global"

type Service struct {
	rdb *redis.Client
}

func New(rdb *redis.Client) *Service {
	return &Service{rdb: rdb}
}

type Entry struct {
	Rank  int64   `json:"rank"`
	User  string  `json:"user"`
	Score float64 `json:"score"`
}

type Rank struct {
	Rank  int64   `json:"rank"`
	Score float64 `json:"score"`
}

// AddScore はスコアを加算する。ZINCRBY は原子操作なので、
// 複数クライアントが同時に叩いてもアプリ側でロックが要らない。
// ← シングルスレッド設計の恩恵をここで体感する。
func (s *Service) AddScore(ctx context.Context, user string, points float64) (float64, error) {
	return s.rdb.ZIncrBy(ctx, key, points, user).Result()
}

// Top は上位 N 件（順位つき）。降順・スコア付きで取得。
func (s *Service) Top(ctx context.Context, n int64) ([]Entry, error) {
	zs, err := s.rdb.ZRevRangeWithScores(ctx, key, 0, n-1).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(zs))
	for i, z := range zs {
		out = append(out, Entry{
			Rank:  int64(i) + 1,
			User:  z.Member.(string),
			Score: z.Score,
		})
	}
	return out, nil
}

// RankOf は特定ユーザーの順位（1始まり）とスコア。未登録なら nil, nil。
func (s *Service) RankOf(ctx context.Context, user string) (*Rank, error) {
	rank, err := s.rdb.ZRevRank(ctx, key, user).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	score, err := s.rdb.ZScore(ctx, key, user).Result()
	if err != nil {
		return nil, err
	}
	return &Rank{Rank: rank + 1, Score: score}, nil
}

func (s *Service) Count(ctx context.Context) (int64, error) {
	return s.rdb.ZCard(ctx, key).Result()
}
