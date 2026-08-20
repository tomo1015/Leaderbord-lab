package main

import (
	"context"
	_ "embed"
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

//go:embed web/index.html
var dashboardHTML []byte

func main() {
	seedN := flag.Int("seed", 0, "指定件数のランダムユーザーを投入して終了する")
	withCountry := flag.Bool("country", false, "seed 時に各ユーザーへランダムな国を割り当て、国別キーにも二重書き込みする")
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
		seed(ctx, board, *seedN, *withCountry)
		return
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "api": "/leaderboard", "dashboard": "/dashboard"})
	})

	// ダッシュボード（1枚のHTMLを埋め込み配信。同一オリジンなので CORS 不要）
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(dashboardHTML)
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
		country := r.FormValue("country") // 任意。あれば国別キーにも二重書き込み
		points, err := strconv.ParseFloat(r.FormValue("points"), 64)
		if user == "" || err != nil {
			http.Error(w, `{"error":"user と数値の points が必要"}`, http.StatusBadRequest)
			return
		}
		var score float64
		if country != "" {
			score, err = board.AddScoreWithCountry(ctx, user, country, points)
		} else {
			score, err = board.AddScore(ctx, user, points)
		}
		if err != nil {
			httpErr(w, err)
			return
		}
		rank, err := board.RankOf(ctx, user)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"user": user, "country": country, "score": score, "rank": rank})
	})

	// 個人ビュー：指定ユーザーを中心に前後2人（計5人）の窓を返す。
	mux.HandleFunc("GET /leaderboard/me/{user}", func(w http.ResponseWriter, r *http.Request) {
		user := r.PathValue("user")
		around, err := board.Around(ctx, user, 2)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"user": user, "found": around != nil, "around": around})
	})

	// 国別の上位10件。専用キーから引くので件数に不感で O(log N)。
	mux.HandleFunc("GET /leaderboard/country/{country}", func(w http.ResponseWriter, r *http.Request) {
		country := r.PathValue("country")
		top, err := board.TopCountry(ctx, country, 10)
		if err != nil {
			httpErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"country": country, "top": top})
	})

	log.Println("listening on :8000")
	log.Fatal(http.ListenAndServe(":8000", mux))
}

var countries = []string{"jp", "us", "kr", "de", "br"}

func seed(ctx context.Context, board *leaderboard.Service, n int, withCountry bool) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	dist := map[string]int{}
	for i := 1; i <= n; i++ {
		user := "user:" + strconv.Itoa(i)
		pts := float64(r.Intn(1_000_001))
		var err error
		if withCountry {
			c := countries[r.Intn(len(countries))]
			dist[c]++
			_, err = board.AddScoreWithCountry(ctx, user, c, pts)
		} else {
			_, err = board.AddScore(ctx, user, pts)
		}
		if err != nil {
			log.Fatal(err)
		}
		if i%10000 == 0 {
			log.Printf("seeded %d...", i)
		}
	}
	count, _ := board.Count(ctx)
	log.Printf("投入完了: 全体 %d 件", count)
	if withCountry {
		log.Printf("国別の内訳: %v", dist)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, err error) {
	http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusInternalServerError)
}
