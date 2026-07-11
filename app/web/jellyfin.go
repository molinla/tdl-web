package web

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"
)

type jellyfinClient struct {
	base    string
	apiKey  string
	timerMu sync.Mutex
	timer   *time.Timer
}

func newJellyfinClient(base, apiKey string) *jellyfinClient {
	return &jellyfinClient{
		base:   strings.TrimRight(base, "/"),
		apiKey: apiKey,
	}
}

func (j *jellyfinClient) RefreshSoon(ctx context.Context) {
	j.timerMu.Lock()
	defer j.timerMu.Unlock()
	if j.timer != nil {
		j.timer.Stop()
	}
	j.timer = time.AfterFunc(30*time.Second, func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.base+"/Library/Refresh", nil)
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "MediaBrowser Token="+j.apiKey)
		req.Header.Set("X-Emby-Token", j.apiKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	})
}
