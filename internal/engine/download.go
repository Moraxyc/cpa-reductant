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

	"github.com/moraxyc/cpa-reductant/internal/config"
	"github.com/yetone/magpie/pkg/guard"
)

const (
	fallbackMirrorHost = "hf-mirror.com"
	downloadRetryWait  = time.Hour
	downloadStallWait  = 60 * time.Second
)

// weightProvisioner decouples on-disk weight layout resolution and async
// downloading from modelRunner's inference scheduling.
type weightProvisioner interface {
	Resolve(dir string) (resolvedDir, layout string, installed bool)
	Stamp(dir string) string
	EnsureAsync(dir, mirror string, onReady func())
	ForceAsync(dir, mirror string, onReady func())
	Reset()
	Cancel()
	Status() (downloading bool, done, total int64, errStr string)
	SetLogger(fn func(level, msg string, fields map[string]any))
}

func versionedModelDir(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, guard.Rev[:12])
}

func modelInstalled(dir string) bool {
	_, ok := resolveModelDir(dir)
	return ok
}

// resolveModelDir locates the directory containing the model weights: first
// checking the pinned revision subdirectory (<dir>/<rev[:12]>, matching
// Magpie's layout and ensuring exact pinned byte sizes), then falling back to
// flat files directly inside <dir> for manual placement.
func resolveModelDir(dir string) (string, bool) {
	resolved, _, ok := resolveModelDirWithLayout(dir)
	return resolved, ok
}

func resolveModelDirWithLayout(dir string) (string, string, bool) {
	if dir == "" {
		return "", "missing", false
	}
	vdir := versionedModelDir(dir)
	if hasPinnedFiles(vdir) {
		return vdir, "versioned", true
	}
	if hasFlatFiles(dir) {
		return dir, "flat", true
	}
	return "", "missing", false
}

func hasPinnedFiles(dir string) bool {
	if dir == "" {
		return false
	}
	for _, f := range guard.PinnedFiles {
		fi, err := os.Stat(filepath.Join(dir, filepath.Base(f.Path)))
		if err != nil || fi.IsDir() || fi.Size() != f.Size {
			return false
		}
	}
	return true
}

func hasFlatFiles(dir string) bool {
	for _, rel := range guard.Files {
		fi, err := os.Stat(filepath.Join(dir, filepath.Base(rel)))
		if err != nil || fi.IsDir() || fi.Size() == 0 {
			return false
		}
	}
	return true
}

func modelDirStamp(dir string) string {
	h := sha256.New()
	for _, rel := range guard.Files {
		name := filepath.Base(rel)
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s:%d:%d\n", name, fi.Size(), fi.ModTime().UnixNano())
	}
	sum := h.Sum(nil)
	return fmt.Sprintf("%x", sum[:6])
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
	logFn     func(level, msg string, fields map[string]any)
}

func (d *modelDownloader) SetLogger(fn func(level, msg string, fields map[string]any)) {
	d.mu.Lock()
	d.logFn = fn
	d.mu.Unlock()
}

func (d *modelDownloader) Resolve(dir string) (string, string, bool) {
	return resolveModelDirWithLayout(dir)
}

func (d *modelDownloader) Stamp(dir string) string {
	return modelDirStamp(dir)
}

func (d *modelDownloader) Status() (downloading bool, done, total int64, errStr string) {
	return d.status()
}

func (d *modelDownloader) Trigger(cfg config.ModelConfig, mirrorOverride string, onReady func(dir string)) error {
	if cfg.IsRemoteHTTP() {
		return errors.New("model download is only supported for local astrlink-guard")
	}
	dir := cfg.EffectiveDir()
	if dir == "" {
		return errors.New("model directory could not be resolved; set model.dir or CPA_REDUCTANT_MODEL_DIR")
	}
	if _, _, installed := d.Resolve(dir); installed {
		if onReady != nil {
			onReady(dir)
		}
		return nil
	}
	mirror := strings.TrimSpace(mirrorOverride)
	if mirror == "" {
		mirror = cfg.Mirror
	}
	d.ForceAsync(dir, mirror, func() {
		if onReady != nil {
			onReady(dir)
		}
	})
	return nil
}

func (d *modelDownloader) logf(level, msg string, fields map[string]any) {
	d.mu.Lock()
	fn := d.logFn
	d.mu.Unlock()
	if fn != nil {
		fn(level, msg, fields)
		return
	}
	log.Printf("cpa-reductant: %s", msg)
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
	d.startAsync(dir, mirror, false, onReady)
}

func (d *modelDownloader) ForceAsync(dir, mirror string, onReady func()) {
	d.startAsync(dir, mirror, true, onReady)
}

func (d *modelDownloader) startAsync(dir, mirror string, force bool, onReady func()) {
	if dir == "" || modelInstalled(dir) {
		return
	}
	d.mu.Lock()
	if d.cancel != nil {
		d.mu.Unlock()
		return
	}
	if !force && d.activeDir == dir && !d.lastTriedAt.IsZero() && time.Since(d.lastTriedAt) < downloadRetryWait {
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

	d.logf("info", fmt.Sprintf("downloading astrlink-guard (%s)", guard.Rev[:12]), map[string]any{
		"component": "model_downloader",
		"rev":       guard.Rev[:12],
		"dir":       versionedModelDir(dir),
	})
	go func() {
		defer d.wg.Done()
		defer cancel()
		err := d.install(ctx, dir, mirror)
		var logLevel, logMsg string
		var logFields map[string]any
		d.mu.Lock()
		d.cancel = nil
		if err != nil {
			if ctx.Err() != nil {
				d.lastErr = ""
			} else {
				d.lastErr = err.Error()
				logLevel = "warn"
				logMsg = fmt.Sprintf("astrlink-guard download failed: %v", err)
				logFields = map[string]any{"component": "model_downloader", "error": err.Error()}
			}
		} else {
			d.lastErr = ""
			logLevel = "info"
			logMsg = "astrlink-guard weights ready"
			logFields = map[string]any{"component": "model_downloader", "dir": versionedModelDir(dir)}
		}
		d.mu.Unlock()
		if logMsg != "" {
			d.logf(logLevel, logMsg, logFields)
		}
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
