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

// countryKeyは国別リーダーボードのキー名
// 例：countryKey("jp") -> "leaderboard:jp"
func countryKey(country string) string {
	return "leaderboard:" + country
}

// AddScore はスコアを加算する。ZINCRBY は原子操作なので、
// 複数クライアントが同時に叩いてもアプリ側でロックが要らない。
// ← シングルスレッド設計の恩恵をここで体感する。
func (s *Service) AddScore(ctx context.Context, user string, points float64) (float64, error) {
	return s.rdb.ZIncrBy(ctx, key, points, user).Result()
}

// AddScoreWithCountry は「実験3の解決策」。1回のスコア更新で
// 全体キー(leaderboard:global)と国別キー(leaderboard:<country>)の両方へ同時に加算する。
// TxPipeline で2本の ZINCRBY を1往復にまとめ、まとめて実行する。
//
// ★裏面のトレードオフ：読みを速くする代わりに、書き込みが2倍・データも二重になる。
//
//	「読み最適化のために書き込みとメモリを払う」という Redis 設計の定番パターン。
func (s *Service) AddScoreWithCountry(ctx context.Context, user, country string, points float64) (float64, error) {
	pipe := s.rdb.TxPipeline()
	globalCmd := pipe.ZIncrBy(ctx, key, points, user)
	pipe.ZIncrBy(ctx, countryKey(country), points, user)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return globalCmd.Val(), nil
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

// TopCountry は指定国の上位 N 件。専用キーから引くので件数に関係なく O(log N)。
func (s *Service) TopCountry(ctx context.Context, country string, n int64) ([]Entry, error) {
	return s.topOf(ctx, countryKey(country), n)
}

// topOf は任意のキーの上位 N 件（順位つき・降順・スコア付き）を返す共通処理。
func (s *Service) topOf(ctx context.Context, k string, n int64) ([]Entry, error) {
	zs, err := s.rdb.ZRevRangeWithScores(ctx, k, 0, n-1).Result()
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
