// 実験A：大量の同時アクセスを捌く（単一ノードの限界を測る）。
//
// 並行度(goroutine数)を段階的に上げながら、各段階で rps とレイテンシ分布
// (p50/p99/p999/max) を測る。-heavy を付けると測定中に重いコマンド(ZRANGE 0 -1)を
// 定期的に差し込み、実験5の head-of-line blocking が同時アクセス下でテールをどう
// 悪化させるかを観察できる。
//
// 実行例:
//
//	docker compose exec app go run ./cmd/load -op read -c 1,8,32,128,512 -d 3s
//	docker compose exec app go run ./cmd/load -op read -c 1,8,32,128,512 -d 3s -heavy
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const globalKey = "leaderboard:global"

func main() {
	addr := envOr("REDIS_ADDR", "localhost:6379")
	dur := flag.Duration("d", 3*time.Second, "各並行度での測定時間")
	levelsCSV := flag.String("c", "1,8,32,128,512", "並行度(goroutine数)のスイープ、カンマ区切り")
	op := flag.String("op", "read", "操作: read(上位10取得 ZREVRANGE 0 9) / write(ZINCRBY)")
	heavy := flag.Bool("heavy", false, "測定中に重いコマンド(ZRANGE 0 -1)を定期的に差し込む")
	heavyEvery := flag.Duration("heavy-every", 200*time.Millisecond, "重いコマンドを差し込む間隔（短いほど巻き添えが頻繁）")
	flag.Parse()

	levels := parseLevels(*levelsCSV)
	maxC := 1
	for _, l := range levels {
		if l > maxC {
			maxC = l
		}
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		PoolSize: maxC + 16, // 並行度ぶんの接続を確保（プール枯渇でクライアント側が詰まるのを防ぐ）
	})
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis接続失敗: %v", err)
	}
	card, _ := rdb.ZCard(ctx, globalKey).Result()
	fmt.Printf("対象キー %s のメンバー数: %d\n", globalKey, card)
	fmt.Printf("操作: %s / 各%v測定 / heavy注入: %v\n\n", *op, *dur, *heavy)

	fmt.Printf("%-8s %-12s %-9s %-9s %-9s %-9s\n", "並行度", "rps", "p50(ms)", "p99(ms)", "p999(ms)", "max(ms)")
	fmt.Println(strings.Repeat("-", 62))
	for _, c := range levels {
		rps, p50, p99, p999, mx := runLevel(ctx, rdb, c, *dur, *op, *heavy, *heavyEvery)
		fmt.Printf("%-8d %-12.0f %-9.2f %-9.2f %-9.2f %-9.2f\n", c, rps, p50, p99, p999, mx)
	}
}

func runLevel(ctx context.Context, rdb *redis.Client, c int, d time.Duration, op string, heavy bool, heavyEvery time.Duration) (rps, p50, p99, p999, mx float64) {
	perWorker := make([][]time.Duration, c)
	deadline := time.Now().Add(d)

	stopHeavy := make(chan struct{})
	if heavy {
		go func() {
			t := time.NewTicker(heavyEvery)
			defer t.Stop()
			for {
				select {
				case <-stopHeavy:
					return
				case <-t.C:
					_, _ = rdb.ZRange(ctx, globalKey, 0, -1).Result() // 重い1本
				}
			}
		}()
	}

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < c; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)))
			var lat []time.Duration
			for time.Now().Before(deadline) {
				t0 := time.Now()
				doOp(ctx, rdb, op, r)
				lat = append(lat, time.Since(t0))
			}
			perWorker[id] = lat
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	if heavy {
		close(stopHeavy)
	}

	var all []time.Duration
	for _, w := range perWorker {
		all = append(all, w...)
	}
	total := len(all)
	rps = float64(total) / elapsed.Seconds()
	if total == 0 {
		return
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	p50 = msOf(pct(all, 0.50))
	p99 = msOf(pct(all, 0.99))
	p999 = msOf(pct(all, 0.999))
	mx = msOf(all[total-1])
	return
}

func doOp(ctx context.Context, rdb *redis.Client, op string, r *rand.Rand) {
	switch op {
	case "write":
		_ = rdb.ZIncrBy(ctx, globalKey, float64(r.Intn(100)), "user:"+strconv.Itoa(r.Intn(100000)+1)).Err()
	default: // read
		_, _ = rdb.ZRevRangeWithScores(ctx, globalKey, 0, 9).Result()
	}
}

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func msOf(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseLevels(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		out = []int{1}
	}
	return out
}
