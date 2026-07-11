package cmd

import (
	"context"
	"fmt"
	"math"
	"net"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	appweb "github.com/iyear/tdl/app/web"
	"github.com/iyear/tdl/core/logctx"
	"github.com/iyear/tdl/core/storage"
	"github.com/iyear/tdl/pkg/consts"
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
			opts.Template = viper.GetString(consts.FlagDlTemplate)
			if err := applyWebRange(&opts, input); err != nil {
				return err
			}
			return tRun(cmd.Context(), func(ctx context.Context, c *telegram.Client, kvd storage.Storage) error {
				return appweb.Run(logctx.Named(ctx, "web"), c, kvd, opts)
			})
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
