package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	appweb "github.com/iyear/tdl/app/web"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/core/storage/keygen"
	"github.com/iyear/tdl/pkg/consts"
	"github.com/iyear/tdl/pkg/tclient"
)

const (
	webActiveSessionStorageKey   = "web:active-session"
	webPreviousSessionStorageKey = "web:previous-session"
)

func NewWeb() *cobra.Command {
	var (
		opts  appweb.Options
		input []int
	)

	cmd := &cobra.Command{
		Use:     "web",
		Short:   "Start web API for JSON based download and preview (use separate web/ frontend)",
		GroupID: groupTools.ID,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateWebListen(opts.Addr, opts.WebToken); err != nil {
				return err
			}
			applyWebReconnectDefault(cmd)
			opts.Template = viper.GetString(consts.FlagDlTemplate)
			if err := applyWebRange(&opts, input); err != nil {
				return err
			}
			return runWeb(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringSliceVarP(&opts.Files, "file", "f", []string{}, "official client or tdl exported JSON files")
	cmd.Flags().StringSliceVarP(&opts.URLs, "url", "u", []string{}, "telegram message links")
	cmd.Flags().StringVarP(&opts.Dir, "dir", "d", "downloads", "download directory")
	cmd.Flags().StringVar(&opts.CacheDir, "cache-dir", "", "web cache directory, defaults to ~/.tdl/web-cache")
	cmd.Flags().StringVar(&opts.Addr, "addr", "127.0.0.1:8080", "web listen address")
	cmd.Flags().String(consts.FlagDlTemplate, `{{ .DialogID }}_{{ .MessageID }}_{{ filenamify .FileName }}`, "download file name template")
	cmd.Flags().BoolVar(&opts.Continue, "continue", false, "reuse existing tdl download resume state")
	cmd.Flags().BoolVar(&opts.SkipSame, "skip-same", false, "skip files with the same name(without extension) and size")
	cmd.Flags().BoolVar(&opts.Desc, "desc", false, "download/list files from newest to oldest")
	cmd.Flags().BoolVar(&opts.Takeout, "takeout", false, "takeout sessions let you export data from your account with lower flood wait limits")
	cmd.Flags().StringVar(&opts.WebToken, "web-token", "", "required token for non-local web access")
	cmd.Flags().StringVar(&opts.JellyfinURL, "jellyfin-url", "", "Jellyfin server URL")
	cmd.Flags().StringVar(&opts.JellyfinAPIKey, "jellyfin-api-key", "", "Jellyfin API key")
	cmd.Flags().BoolVar(&opts.JellyfinRefresh, "jellyfin-refresh", false, "refresh Jellyfin library after downloads complete")
	cmd.Flags().StringVar(&opts.JellyfinLibraryDir, "jellyfin-library-path", "", "Jellyfin media library path; defaults to --dir when empty")
	cmd.Flags().StringVar(&opts.RangeType, "type", "", "import range type: id or time (empty = no filter)")
	cmd.Flags().IntSliceVarP(&input, "input", "i", []int{}, "range from,to for --type id|time")
	cmd.Flags().BoolVar(&opts.RefreshMeta, "refresh-meta", false, "ignore parsed metadata cache and re-fetch from Telegram")

	_ = viper.BindPFlag(consts.FlagDlTemplate, cmd.Flags().Lookup(consts.FlagDlTemplate))
	_ = cmd.RegisterFlagCompletionFunc("file", completeExtFiles("json"))
	_ = cmd.MarkFlagDirname("dir")
	_ = cmd.MarkFlagDirname("cache-dir")
	return cmd
}

func runWeb(ctx context.Context, opts appweb.Options) error {
	base, err := tOptions(ctx)
	if err != nil {
		return fmt.Errorf("build telegram options: %w", err)
	}
	base.DeviceModel = "BOC Preview"
	sessionKey, err := currentWebSession(ctx, base.KV)
	if err != nil {
		return fmt.Errorf("load active web session: %w", err)
	}

	for {
		_, canSwitchBack, err := previousWebSession(ctx, base.KV)
		if err != nil {
			return fmt.Errorf("load previous web session: %w", err)
		}
		d := tg.NewUpdateDispatcher()
		o := base
		o.UpdateHandler = d
		client, err := newWebClient(ctx, o, sessionKey)
		if err != nil {
			return fmt.Errorf("create client: %w", err)
		}
		err = client.Run(ctx, func(ctx context.Context) error {
			return appweb.Run(
				logctx.Named(ctx, "web"),
				client,
				o.KV,
				opts,
				qrlogin.OnLoginToken(d),
				d,
				canSwitchBack,
			)
		})
		var switchErr *appweb.AccountSwitchError
		if errors.As(err, &switchErr) {
			if switchErr.Previous {
				if switchErr.DiscardCurrent {
					sessionKey, err = cancelWebSession(ctx, base.KV, sessionKey)
				} else {
					sessionKey, err = usePreviousWebSession(ctx, base.KV, sessionKey)
				}
			} else {
				sessionKey, err = newWebSession(ctx, base.KV, sessionKey)
			}
			if err != nil {
				return fmt.Errorf("switch web session: %w", err)
			}
			continue
		}
		if auth.IsUnauthorized(err) {
			continue
		}
		return err
	}
}

func newWebClient(ctx context.Context, o tclient.Options, sessionKey string) (*telegram.Client, error) {
	session := storage.NewSession(o.KV, false)
	if sessionKey != "" {
		session = storage.NewSessionWithKey(o.KV, false, sessionKey)
	}
	return tclient.NewWithSession(ctx, o, session)
}

func currentWebSession(ctx context.Context, kvd storage.Storage) (string, error) {
	value, err := kvd.Get(ctx, webActiveSessionStorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	return string(value), err
}

func previousWebSession(ctx context.Context, kvd storage.Storage) (string, bool, error) {
	value, err := kvd.Get(ctx, webPreviousSessionStorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		return "", false, nil
	}
	return string(value), err == nil, err
}

func newWebSession(ctx context.Context, kvd storage.Storage, previous string) (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	key := keygen.New("web", "session", hex.EncodeToString(token[:]))
	if err := kvd.Set(ctx, webPreviousSessionStorageKey, []byte(previous)); err != nil {
		return "", err
	}
	return key, kvd.Set(ctx, webActiveSessionStorageKey, []byte(key))
}

func usePreviousWebSession(ctx context.Context, kvd storage.Storage, current string) (string, error) {
	previous, ok, err := previousWebSession(ctx, kvd)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("no previous web session")
	}
	if err := kvd.Set(ctx, webPreviousSessionStorageKey, []byte(current)); err != nil {
		return "", err
	}
	return previous, kvd.Set(ctx, webActiveSessionStorageKey, []byte(previous))
}

func cancelWebSession(ctx context.Context, kvd storage.Storage, current string) (string, error) {
	previous, ok, err := previousWebSession(ctx, kvd)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("no previous web session")
	}
	if previous == "" {
		if err := kvd.Delete(ctx, webActiveSessionStorageKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return "", err
		}
	} else if err := kvd.Set(ctx, webActiveSessionStorageKey, []byte(previous)); err != nil {
		return "", err
	}
	if err := kvd.Delete(ctx, webPreviousSessionStorageKey); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", err
	}
	if current != "" {
		if err := kvd.Delete(ctx, current); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return "", err
		}
	}
	return previous, nil
}

func applyWebReconnectDefault(cmd *cobra.Command) {
	// The web API is a long-running process. The global default is finite so
	// one-shot CLI commands do not hang forever, but for web mode that means a
	// network drop, poor connection, or system sleep after the timeout can stop
	// the whole API process. Use gotd's infinite reconnection mode by default,
	// while still respecting explicit CLI/env configuration.
	if flag := cmd.Root().PersistentFlags().Lookup(consts.FlagReconnectTimeout); flag != nil && flag.Changed {
		return
	}
	if _, ok := os.LookupEnv("TDL_RECONNECT_TIMEOUT"); ok {
		return
	}
	viper.Set(consts.FlagReconnectTimeout, time.Duration(0))
}

func applyWebRange(opts *appweb.Options, input []int) error {
	typ := strings.ToLower(strings.TrimSpace(opts.RangeType))
	opts.RangeType = typ
	if typ == "" {
		return nil
	}
	if typ != appweb.RangeTypeID && typ != appweb.RangeTypeTime {
		return fmt.Errorf("invalid --type %q, want id or time", opts.RangeType)
	}
	switch len(input) {
	case 0:
		opts.RangeFrom = 0
		opts.RangeTo = math.MaxInt
	case 1:
		opts.RangeFrom = input[0]
		opts.RangeTo = math.MaxInt
	case 2:
		opts.RangeFrom = input[0]
		opts.RangeTo = input[1]
	default:
		return fmt.Errorf("--input should be at most 2 integers for --type %s", typ)
	}
	if opts.RangeFrom > opts.RangeTo {
		opts.RangeFrom, opts.RangeTo = opts.RangeTo, opts.RangeFrom
	}
	return nil
}

func validateWebListen(addr, token string) error {
	if token != "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr: %w", err)
	}
	if host == "" {
		return fmt.Errorf("--web-token is required when --addr is not loopback")
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	return fmt.Errorf("--web-token is required when --addr is not loopback")
}
