package engine

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/yetone/magpie/pkg/guard"
	"github.com/yetone/magpie/pkg/redact"
)

const (
	// Binary cache magic matching Magpie's pfound02 schema: stores SHA-256 text
	// hashes and span offsets/scores/labels only (never raw plaintext).
	diskCacheMagic = "pfound02"
	saveAfter      = 2 * time.Second
)

var errBadDiskCache = errors.New("invalid model disk cache")

func diskCachePath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "found-"+guard.Rev[:12]+".bin")
}

type diskCache struct {
	mu        sync.Mutex
	loadedDir string
	dirty     bool
	timer     *time.Timer
	savingMu  sync.Mutex
	wg        sync.WaitGroup
}

func (d *diskCache) loadOnce(dir string, cache *textLRU) {
	if dir == "" {
		return
	}
	d.mu.Lock()
	if d.loadedDir == dir {
		d.mu.Unlock()
		return
	}
	d.loadedDir = dir
	d.mu.Unlock()

	raw, err := os.ReadFile(diskCachePath(dir))
	if err != nil {
		return
	}
	entries, err := decodeDiskCache(raw)
	if err != nil {
		return
	}
	cache.seed(entries)
}

func (d *diskCache) scheduleSave(dir string, cache *textLRU) {
	if dir == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dirty = true
	if d.timer != nil {
		return
	}
	d.wg.Add(1)
	d.timer = time.AfterFunc(saveAfter, func() {
		defer d.wg.Done()
		d.mu.Lock()
		d.timer = nil
		targetDir := d.loadedDir
		if targetDir == "" {
			targetDir = dir
		}
		dirty := d.dirty
		d.dirty = false
		d.mu.Unlock()
		if dirty {
			_ = d.writeFile(targetDir, cache)
		}
	})
}

func (d *diskCache) flush(dir string, cache *textLRU) {
	d.mu.Lock()
	if d.timer != nil {
		if d.timer.Stop() {
			d.wg.Done()
		}
		d.timer = nil
	}
	targetDir := dir
	if targetDir == "" {
		targetDir = d.loadedDir
	}
	dirty := d.dirty
	d.dirty = false
	d.mu.Unlock()

	d.wg.Wait()
	if dirty && targetDir != "" {
		_ = d.writeFile(targetDir, cache)
	}
}

func (d *diskCache) reset() {
	d.mu.Lock()
	if d.timer != nil {
		if d.timer.Stop() {
			d.wg.Done()
		}
		d.timer = nil
	}
	d.loadedDir = ""
	d.dirty = false
	d.mu.Unlock()
	d.wg.Wait()
}

func (d *diskCache) writeFile(dir string, cache *textLRU) error {
	path := diskCachePath(dir)
	if path == "" {
		return nil
	}
	d.savingMu.Lock()
	defer d.savingMu.Unlock()

	entries := cache.snapshotOldestFirst()
	if len(entries) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data := encodeDiskCache(entries)
	tmp, err := os.CreateTemp(dir, "found-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmpName, path)
	}
	if err != nil {
		_ = os.Remove(tmpName)
	}
	return err
}

func encodeDiskCache(entries []lruEntry) []byte {
	var b bytes.Buffer
	b.WriteString(diskCacheMagic)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(entries)))
	for _, en := range entries {
		b.Write(en.k[:])
		count := min(len(en.fs), 0xffff)
		_ = binary.Write(&b, binary.LittleEndian, uint16(count))
		for _, f := range en.fs[:count] {
			label := f.Label
			if len(label) > 255 {
				label = label[:255]
			}
			_ = binary.Write(&b, binary.LittleEndian, uint32(f.Start))
			_ = binary.Write(&b, binary.LittleEndian, uint32(f.End))
			_ = binary.Write(&b, binary.LittleEndian, float32(f.Score))
			b.WriteByte(byte(len(label)))
			b.WriteString(label)
		}
	}
	return b.Bytes()
}

func decodeDiskCache(b []byte) ([]lruEntry, error) {
	if !bytes.HasPrefix(b, []byte(diskCacheMagic)) {
		return nil, errBadDiskCache
	}
	r := bufio.NewReader(bytes.NewReader(b[len(diskCacheMagic):]))
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n > uint32(len(b)/34) {
		return nil, errBadDiskCache
	}
	out := make([]lruEntry, 0, n)
	for range n {
		var e lruEntry
		if _, err := io.ReadFull(r, e.k[:]); err != nil {
			return nil, err
		}
		var spans uint16
		if err := binary.Read(r, binary.LittleEndian, &spans); err != nil {
			return nil, err
		}
		if spans > 0 {
			e.fs = make([]redact.Found, 0, spans)
		}
		for range spans {
			var se [2]uint32
			if err := binary.Read(r, binary.LittleEndian, &se); err != nil {
				return nil, err
			}
			var score float32
			if err := binary.Read(r, binary.LittleEndian, &score); err != nil {
				return nil, err
			}
			if !(score >= 0 && score <= 1) {
				return nil, errBadDiskCache
			}
			l, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			label := make([]byte, l)
			if _, err := io.ReadFull(r, label); err != nil {
				return nil, err
			}
			if se[0] >= se[1] {
				return nil, errBadDiskCache
			}
			e.fs = append(e.fs, redact.Found{
				Start: int(se[0]),
				End:   int(se[1]),
				Label: string(label),
				Score: float64(score),
			})
		}
		out = append(out, e)
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, errBadDiskCache
	}
	return out, nil
}
