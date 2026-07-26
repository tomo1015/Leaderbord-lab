package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"leaderboard-lab/internal/leaderboard"
)

func main() {
	seedN := flag.Int("seed", 0, "指定件数のランダムユーザーを投入して終了する")
	flag.Parse()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	board := leaderboard.New(rdb)
	ctx := context.Background()

	// -seed が指定されたら投入だけして終了（実験2・負荷観察の弾込め用）
	if *seedN > 0 {
		seed(ctx, board, *seedN)
		return
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "try": "/leaderboard"})
	})

	mux.HandleFunc("GET /leaderboard", func(w http.ResponseWriter, r *http.Request) {
		count, err := board.Count(ctx)
		if err != nil {
			httpErr(w, err)
			return
		}
		top, err := board.Top(ctx, 10)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"count": count, "top": top})
	})

	mux.HandleFunc("POST /leaderboard/score", func(w http.ResponseWriter, r *http.Request) {
		user := r.FormValue("user")
		points, err := strconv.ParseFloat(r.FormValue("points"), 64)
		if user == "" || err != nil {
			http.Error(w, `{"error":"user と数値の points が必要"}`, http.StatusBadRequest)
			return
		}
		score, err := board.AddScore(ctx, user, points)
		if err != nil {
			httpErr(w, err)
			return
		}
		rank, err := board.RankOf(ctx, user)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"user": user, "score": score, "rank": rank})
	})

	log.Println("listening on :8000")
	log.Fatal(http.ListenAndServe(":8000", mux))
}

func seed(ctx context.Context, board *leaderboard.Service, n int) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 1; i <= n; i++ {
		user := "user:" + strconv.Itoa(i)
		if _, err := board.AddScore(ctx, user, float64(r.Intn(1_000_001))); err != nil {
			log.Fatal(err)
		}
		if i%10000 == 0 {
			log.Printf("seeded %d...", i)
		}
	}
	count, _ := board.Count(ctx)
	log.Printf("投入完了: 現在 %d 件", count)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
}
