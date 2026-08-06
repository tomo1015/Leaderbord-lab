// 実験2 追加検証：allkeys-lru で「古いキーが丸ごと追い出され、ホットなキーは生き残る」を観察する。
//
// シナリオ：
//  1. 古いシャード old:0, old:1 に書き込む（以後アクセスしない＝LRU的に「古い」）
//  2. ホットシャード hot に大量に書き続ける（メモリ上限に当てる）
//  3. 上限到達時、Redis は最近使われていない old:* から追い出す。hot は書き続けているので残る。
//
// 実行： docker compose exec app go run ./cmd/evict -old 5000 -hot 300000
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"

	"github.com/redis/go-redis/v9"
)

const (
	keyOld0 = "lb:old:0"
	keyOld1 = "lb:old:1"
	keyHot  = "lb:hot"
)

func main() {
	oldN := flag.Int("old", 5000, "各 old シャードに書き込むメンバー数")
	hotN := flag.Int("hot", 300000, "hot シャードに書き込むメンバー数（上限に当てる用）")
	flag.Parse()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx := context.Background()

	// 再現性のため、この実験で使うキーだけ消してから始める
	rdb.Del(ctx, keyOld0, keyOld1, keyHot)

	r := rand.New(rand.NewSource(1)) // 固定シードで再現可能に

	// --- Phase 1: 古いシャードに書く（このあと二度と触らない） ---
	writeN(ctx, rdb, keyOld0, *oldN, r)
	writeN(ctx, rdb, keyOld1, *oldN, r)
	fmt.Println("=== Phase 1 完了：古いシャードを書き込み、以後アクセスしない ===")
	report(ctx, rdb, keyOld0, keyOld1)

	// --- Phase 2: ホットシャードに書き続ける（メモリ上限に当てる） ---
	fmt.Printf("=== Phase 2：hot に %d 件書き込み（上限に当ててエビクションを誘発）===\n", *hotN)
	for i := 1; i <= *hotN; i++ {
		member := "u:" + strconv.Itoa(i)
		if err := rdb.ZIncrBy(ctx, keyHot, float64(r.Intn(1_000_001)), member).Err(); err != nil {
			log.Fatalf("hot 書き込み失敗: %v", err)
		}
	}

	fmt.Println("=== Phase 2 完了 ===")
	report(ctx, rdb, keyOld0, keyOld1, keyHot)
}

func writeN(ctx context.Context, rdb *redis.Client, key string, n int, r *rand.Rand) {
	for i := 1; i <= n; i++ {
		member := "u:" + strconv.Itoa(i)
		if err := rdb.ZIncrBy(ctx, key, float64(r.Intn(1_000_001)), member).Err(); err != nil {
			log.Fatalf("%s 書き込み失敗: %v", key, err)
		}
	}
}

func report(ctx context.Context, rdb *redis.Client, keys ...string) {
	for _, k := range keys {
		card, _ := rdb.ZCard(ctx, k).Result()
		exists, _ := rdb.Exists(ctx, k).Result()
		state := "生存"
		if exists == 0 {
			state = "★追い出された(キーごと消滅)"
		}
		fmt.Printf("  %-10s ZCARD=%-8d %s\n", k, card, state)
	}
	ev, _ := rdb.Do(ctx, "INFO", "stats").Text()
	fmt.Println("  evicted_keys:", grep(ev, "evicted_keys:"))
	mem, _ := rdb.Do(ctx, "INFO", "memory").Text()
	fmt.Println("  used_memory_human:", grep(mem, "used_memory_human:"))
	fmt.Println()
}

// INFO 出力から key: の値だけ雑に取り出す
func grep(info, prefix string) string {
	for _, line := range splitLines(info) {
		if len(line) >= len(prefix) && line[:len(prefix)] == prefix {
			return line[len(prefix):]
		}
	}
	return "?"
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '\n' || c == '\r' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
