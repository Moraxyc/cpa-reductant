package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/pkg/guard"
)

const (
	fallbackMirrorHost = "hf-mirror.com"
	downloadRetryWait  = time.Hour
	downloadStallWait  = 60 * time.Second
)

func versionedModelDir(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, guard.Rev[:12])
}

type modelDownloader struct {
	mu          sync.Mutex
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	activeDir   string
	lastErr     string
	lastTriedAt time.Time
	total       int64
	done        atomic.Int64

	client    *http.Client
	files     []guard.PinnedFile
	stallWait time.Duration
	urlsFor   func(mirror string, f guard.PinnedFile) []string
}

func newModelDownloader() *modelDownloader {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &modelDownloader{
		client:    &http.Client{Transport: tr},
		files:     guard.PinnedFiles,
		stallWait: downloadStallWait,
		urlsFor:   defaultModelURLs,
	}
}

func defaultModelURLs(mirror string, f guard.PinnedFile) []string {
	suffix := guard.Rev + "/" + f.Path
	official := guard.RepoBase + suffix
	hfMirror := mirrorBaseURL("https://"+fallbackMirrorHost) + suffix
	m := strings.TrimSpace(mirror)
	if m == "" {
		return []string{official, hfMirror}
	}
	custom := mirrorBaseURL(m) + suffix
	if custom == official {
		return []string{official, hfMirror}
	}
	return []string{custom, official}
}

func mirrorBaseURL(mirror string) string {
	m := strings.TrimRight(strings.TrimSpace(mirror), "/")
	repoPath := "/QuantumNous/astrlink-guard/resolve"
	if u, err := url.Parse(guard.RepoBase); err == nil && u.Path != "" {
		repoPath = strings.TrimRight(u.Path, "/")
	}
	if strings.HasSuffix(m, repoPath) {
		return m + "/"
	}
	return m + repoPath + "/"
}

func (d *modelDownloader) totalSize() int64 {
	var total int64
	for _, f := range d.files {
		total += f.Size
	}
	return total
}

func (d *modelDownloader) status() (downloading bool, done, total int64, errStr string) {
	d.mu.Lock()
	downloading = d.cancel != nil
	total = d.total
	errStr = d.lastErr
	d.mu.Unlock()
	done = d.done.Load()
	return downloading, done, total, errStr
}

func (d *modelDownloader) Reset() {
	d.Cancel()
	d.mu.Lock()
	d.lastErr = ""
	d.lastTriedAt = time.Time{}
	d.done.Store(0)
	d.total = 0
	d.mu.Unlock()
}

func (d *modelDownloader) Cancel() {
	d.mu.Lock()
	cancel := d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	d.wg.Wait()
}

func (d *modelDownloader) EnsureAsync(dir, mirror string, onReady func()) {
	if dir == "" || modelInstalled(dir) {
		return
	}
	d.mu.Lock()
	if d.cancel != nil {
		d.mu.Unlock()
		return
	}
	if d.activeDir == dir && !d.lastTriedAt.IsZero() && time.Since(d.lastTriedAt) < downloadRetryWait {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.activeDir = dir
	d.lastTriedAt = time.Now()
	d.lastErr = ""
	d.total = d.totalSize()
	d.done.Store(0)
	d.wg.Add(1)
	d.mu.Unlock()

	log.Printf("cpa-reductant: downloading astrlink-guard (%s) into %s", guard.Rev[:12], versionedModelDir(dir))
	go func() {
		defer d.wg.Done()
		defer cancel()
		err := d.install(ctx, dir, mirror)
		d.mu.Lock()
		d.cancel = nil
		if err != nil {
			if ctx.Err() != nil {
				d.lastErr = ""
			} else {
				d.lastErr = err.Error()
				log.Printf("cpa-reductant: astrlink-guard download failed: %v", err)
			}
		} else {
			d.lastErr = ""
			log.Printf("cpa-reductant: astrlink-guard weights ready in %s", versionedModelDir(dir))
		}
		d.mu.Unlock()
		if err == nil && onReady != nil {
			onReady()
		}
	}()
}

func (d *modelDownloader) install(ctx context.Context, rootDir, mirror string) error {
	targetDir := versionedModelDir(rootDir)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	var base int64
	for _, f := range d.files {
		dst := filepath.Join(targetDir, filepath.Base(f.Path))
		if fi, err := os.Stat(dst); err == nil && fi.Size() == f.Size {
			if ok, _ := verifyFileSHA(dst, f.SHA); ok {
				base += f.Size
				d.done.Store(base)
				continue
			}
		}
		var fileDone atomic.Int64
		stopProg := make(chan struct{})
		go func(offset int64) {
			t := time.NewTicker(200 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stopProg:
					return
				case <-t.C:
					d.done.Store(offset + fileDone.Load())
				}
			}
		}(base)
		err := d.fetchWithFallback(ctx, d.urlsFor(mirror, f), dst, f.Size, f.SHA, &fileDone)
		close(stopProg)
		if err != nil {
			return fmt.Errorf("download %s: %w", filepath.Base(f.Path), err)
		}
		base += f.Size
		d.done.Store(base)
	}
	removeOldModelRevs(rootDir, guard.Rev[:12])
	return nil
}

func removeOldModelRevs(rootDir, keepRev string) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == keepRev || !isHexRevisionDir(e.Name()) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(rootDir, e.Name()))
	}
}

func isHexRevisionDir(name string) bool {
	if len(name) != 12 {
		return false
	}
	for _, c := range name {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (d *modelDownloader) fetchWithFallback(ctx context.Context, urls []string, dst string, size int64, wantSHA string, done *atomic.Int64) error {
	var errs []error
	for _, rawURL := range urls {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := d.fetchOne(ctx, rawURL, dst, size, wantSHA, done)
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", rawURL, err))
	}
	return errors.Join(errs...)
}

func (d *modelDownloader) fetchOne(ctx context.Context, rawURL, dst string, size int64, wantSHA string, done *atomic.Int64) error {
	part := dst + ".part"
	have := int64(0)
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
		if size >= 0 && have > size {
			_ = os.Remove(part)
			have = 0
		}
	}
	if size < 0 || have < size {
		got, err := d.downloadRange(ctx, rawURL, part, have, done)
		if err != nil {
			return err
		}
		have = got
	}
	done.Store(have)
	if size >= 0 && have != size {
		return fmt.Errorf("got %d bytes, want %d", have, size)
	}
	ok, err := verifyFileSHA(part, wantSHA)
	if err != nil {
		return err
	}
	if !ok {
		_ = os.Remove(part)
		return errors.New("sha256 checksum mismatch")
	}
	return os.Rename(part, dst)
}

func (d *modelDownloader) downloadRange(ctx context.Context, rawURL, part string, from int64, done *atomic.Int64) (int64, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return from, err
	}
	if from > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(from, 10)+"-")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return from, err
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && from > 0:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC
		from = 0
	default:
		return from, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	out, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return from, err
	}
	n := from
	done.Store(n)

	var stalled atomic.Bool
	stallWait := d.stallWait
	if stallWait <= 0 {
		stallWait = downloadStallWait
	}
	stall := time.AfterFunc(stallWait, func() {
		stalled.Store(true)
		cancel()
	})
	defer stall.Stop()

	buf := make([]byte, 256<<10)
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			stall.Reset(stallWait)
			if _, werr := out.Write(buf[:k]); werr != nil {
				_ = out.Close()
				return n, werr
			}
			n += int64(k)
			done.Store(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = out.Close()
			if stalled.Load() {
				return n, fmt.Errorf("download stalled for %s", stallWait)
			}
			return n, rerr
		}
	}
	return n, out.Close()
}

func verifyFileSHA(path, want string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}
