package main

import (
	"bufio"
	"encoding/json"
	"faviconapi/favicon"
	"fmt"
	"github.com/allegro/bigcache"
	"github.com/rs/zerolog/log"
	"go.uber.org/ratelimit"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"time"
)

type ErrorForURL struct {
	URL     string `json:"url"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

type Error struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

type Icon struct {
	Success bool   `json:"success"`
	Url     string `json:"url"`
	Favicon string `json:"favicon"`
}

const MaxDefaultUrlSize = 16384

func main() {
	if len(os.Args) > 1 {
		cli()
		return
	}

	cache, _ := bigcache.NewBigCache(bigcache.DefaultConfig(time.Hour * 24 * 7 * 365)) // cache for a year-ish
	rl := ratelimit.New(100)

	http.HandleFunc("/api/v1/resolve", func(rw http.ResponseWriter, r *http.Request) {
		defaultIcon := r.URL.Query().Get("default_icon")

		log.Info().Str("ip", r.Header.Get("Cf-Connecting-Ip")).Str("ua", r.UserAgent()).Str("path", "/api/v1/resolve").Str("type", "req").Send()

		if rand.IntN(100) >= 99 {
			stats := cache.Stats()
			log.Info().Str("type", "cache").Int64("hits", stats.Hits).Int64("misses", stats.Misses).Int64("in_cache", stats.DelMisses).Send()
		}

		if len(defaultIcon) > MaxDefaultUrlSize {
			rw.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(rw).Encode(Error{
				Error:   "default url may not exceed 2^16 in size",
				Success: false,
			})
			return
		}

		reader := bufio.NewReader(r.Body)
		rw.Header().Add("Content-Type", "application/json")

		eof := false
		var wg sync.WaitGroup

		for !eof {
			url, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					eof = true
				} else {
					panic(err)
				}
			}

			if !eof {
				url = url[:len(url)-1]
			}

			//rl.Take()

			wg.Add(1)

			go func(url string) {
				authority, err := favicon.GetFaviconAuthority(url)
				if err != nil {
					json.NewEncoder(rw).Encode(ErrorForURL{
						URL:   url,
						Error: "bad url",
					})
					wg.Done()
					return
				}

				var icon string
				if bytes, err := cache.Get(url[:authority]); err == nil {
					icon = string(bytes)
				} else {
					rl.Take()

					icon, err = favicon.Resolve(url)
					if err != nil {
						_ = json.NewEncoder(rw).Encode(ErrorForURL{
							URL:   url,
							Error: err.Error(),
						})

						wg.Done()
						return
					}

					_ = cache.Set(url[:authority], []byte(icon))
				}

				if icon == "" {
					icon = defaultIcon
				}

				_ = json.NewEncoder(rw).Encode(Icon{
					Success: true,
					Url:     url,
					Favicon: icon,
				})

				wg.Done()
			}(url)
		}

		wg.Wait()
	})

	err := http.ListenAndServe(":3333", nil)
	if err != nil {
		log.Fatal().Err(err).Msg("startup failed")
	}
}

func cli() {
	for _, arg := range os.Args[1:] {
		resolved, err := favicon.Resolve(arg)

		fmt.Println("=== " + arg)
		fmt.Printf("   resolved: %s\n", resolved)
		fmt.Printf("   err: %s\n", err)
		fmt.Println("===")
	}
}
