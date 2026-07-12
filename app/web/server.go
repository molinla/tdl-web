package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	"github.com/go-faster/errors"
	"github.com/gorilla/mux"
	"github.com/gotd/td/telegram"
	"github.com/spf13/viper"
	"go.uber.org/multierr"

	"github.com/iyear/tdl/core/dcpool"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/tclient"
	"github.com/iyear/tdl/pkg/consts"
)

func Run(ctx context.Context, c *telegram.Client, kvd storage.Storage, opts Options) (rerr error) {
	if opts.Addr == "" {
		opts.Addr = "127.0.0.1:8080"
	}
	if opts.Dir == "" {
		opts.Dir = "downloads"
	}
	if opts.JellyfinLibraryDir != "" {
		opts.Dir = opts.JellyfinLibraryDir
	}
	if opts.CacheDir == "" {
		opts.CacheDir = filepath.Join(consts.DataDir, "web-cache")
	}
	if opts.Template == "" {
		opts.Template = `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`
	}
	if err := normalizeRange(&opts); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.Dir, defaultCachePerm); err != nil {
		return errors.Wrap(err, "create download dir")
	}
	if err := os.MkdirAll(opts.CacheDir, defaultCachePerm); err != nil {
		return errors.Wrap(err, "create cache dir")
	}

	pool := dcpool.NewPool(c,
		int64(viper.GetInt(consts.FlagPoolSize)),
		tclient.NewDefaultMiddlewares(ctx, viper.GetDuration(consts.FlagReconnectTimeout))...)
	defer multierr.AppendInvoke(&rerr, multierr.Close(pool))

	s := &Server{
		opts:         opts,
		pool:         pool,
		kvd:          kvd,
		ctx:          ctx,
		items:        map[string]*Item{},
		finished:     map[int]struct{}{},
		downloading:  map[string]struct{}{},
		dlPriority:   map[string]bool{},
		preempted:    map[string]struct{}{},
		cancels:      map[string]context.CancelFunc{},
		events:       make(chan struct{}, 1),
		importPhase:  phaseIdle,
		importSource: sourceIdle,
	}
	if opts.JellyfinRefresh && opts.JellyfinURL != "" && opts.JellyfinAPIKey != "" {
		s.jelly = newJellyfinClient(opts.JellyfinURL, opts.JellyfinAPIKey)
	}

	router := mux.NewRouter()
	router.Use(corsMiddleware)
	api := router.PathPrefix("/api").Subrouter()
	api.Use(s.authMiddleware)
	api.HandleFunc("/import", s.handleImport(ctx)).Methods(http.MethodPost, http.MethodOptions)
	api.HandleFunc("/items", s.handleItems).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/items/download", s.handleDownload(ctx)).Methods(http.MethodPost, http.MethodOptions)
	api.HandleFunc("/items/{id}/thumb", s.handleThumb(ctx)).Methods(http.MethodGet, http.MethodHead, http.MethodOptions)
	api.HandleFunc("/items/{id}/preview", s.handlePreview(ctx)).Methods(http.MethodGet, http.MethodHead, http.MethodOptions)
	api.HandleFunc("/items/{id}/stream", s.handleStream(ctx)).Methods(http.MethodGet, http.MethodHead, http.MethodOptions)
	api.HandleFunc("/items/{id}/cache", s.handleCache(ctx)).Methods(http.MethodPost, http.MethodOptions)
	api.HandleFunc("/items/{id}/pause", s.handlePause).Methods(http.MethodPost, http.MethodOptions)
	api.HandleFunc("/items/{id}/download", s.handleFileDownload).Methods(http.MethodGet, http.MethodHead, http.MethodOptions)
	api.HandleFunc("/events", s.handleEvents).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	}).Methods(http.MethodGet, http.MethodOptions)

	httpServer := &http.Server{Addr: opts.Addr, Handler: router}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	color.Green("tdl web API serving on http://%s", opts.Addr)
	color.Cyan("Open the separate web/ frontend (npm run dev) and point it at this API")
	if len(opts.Files) > 0 || len(opts.URLs) > 0 {
		go func() {
			s.setImporting(true, "")
			if len(opts.Files) > 0 {
				s.setImportPhase(phaseParseJSON, sourceJSON, "读取 JSON 导出")
			} else {
				s.setImportPhase(phaseFetchMessages, sourceTelegram, "从 Telegram 拉取消息")
			}
			if err := s.importSources(ctx, opts.Files, opts.URLs, opts.RangeType, opts.RangeFrom, opts.RangeTo); err != nil {
				s.setImporting(false, err.Error())
				return
			}
			s.setImporting(false, "")
		}()
	}
	return httpServer.ListenAndServe()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Web-Token, Range")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, Content-Type, Content-Length, Accept-Ranges, Content-Range")
		if origin != "*" {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if s.opts.WebToken != "" {
			token := r.Header.Get("X-Web-Token")
			if token == "" {
				token = r.URL.Query().Get("token")
			}
			if token != s.opts.WebToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) setImporting(importing bool, err string) {
	s.mu.Lock()
	s.importing = importing
	s.importError = err
	if !importing {
		s.importPhase = phaseIdle
		s.importSource = sourceIdle
		s.importDetail = ""
	}
	s.mu.Unlock()
	s.notify()
}

func (s *Server) notify() {
	select {
	case s.events <- struct{}{}:
	default:
	}
}

func (s *Server) itemList() []*Item {
	queuePos := s.queuePositions()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.itemListLocked(queuePos)
}
