package understudy

import (
	"archive/zip"
	"context"
	"crypto/sha1" // #nosec G505 -- Minecraft specifies SHA-1 for resource-pack integrity.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blocktopiaworld/understudy-client/internal/nbt"
	"github.com/blocktopiaworld/understudy-client/protocol"
)

const (
	packLoaded int32 = iota
	packDeclined
	packFailedDownload
	packAccepted
	packDownloaded
	packInvalidURL
	packFailedReload
)

const maxPackBytes = 64 << 20

// Resource pack work is independent of the packet reader: a slow HTTP server
// must not delay keepalives. mu also serializes status writes with the
// configuration -> play acknowledgement, so statuses use the correct packet ID.
type resourcePacks struct {
	mu     sync.Mutex
	jobs   map[protocol.UUID]*packJob
	closed bool
	active int
	wg     sync.WaitGroup
}
type packJob struct{ cancel context.CancelFunc }

func (c *Client) stopResourcePacks() {
	c.packs.mu.Lock()
	c.packs.closed = true
	for _, job := range c.packs.jobs {
		job.cancel()
	}
	c.packs.mu.Unlock()
	c.packs.wg.Wait()
}

func (c *Client) packStatus(id protocol.UUID, status int32) error {
	packetID := c.v.Packets.SBPlayResourcePack
	if c.State() == protocol.StateConfiguration {
		packetID = c.v.Packets.SBConfigResourcePack
	}
	return c.conn.WritePacket(protocol.NewWriter(packetID).UUID(id).VarInt(status).Bytes())
}

func (c *Client) handleResourcePack(ctx context.Context, p protocol.Packet, config bool) (bool, error) {
	push, pop := c.v.Packets.CBPlayResourcePackPush, c.v.Packets.CBPlayResourcePackPop
	if config {
		push, pop = c.v.Packets.CBConfigResourcePackPush, c.v.Packets.CBConfigResourcePackPop
	}
	if p.ID != push && p.ID != pop {
		return false, nil
	}
	r := p.Reader()
	c.packs.mu.Lock()
	defer c.packs.mu.Unlock()
	if c.packs.closed {
		return true, errors.New("understudy: resource pack session closed")
	}
	if p.ID == pop {
		all := !r.Bool()
		var id protocol.UUID
		if !all {
			id = r.UUID()
		}
		if err := packPacketEnd(r); err != nil {
			return true, err
		}
		for key, job := range c.packs.jobs {
			if all || key == id {
				job.cancel()
				delete(c.packs.jobs, key)
			}
		}
		return true, nil
	}
	id, address, hash := r.UUID(), r.String(), r.String()
	_ = r.Bool() // Required packs use the same policy; a decline may disconnect us.
	if r.Bool() {
		n, err := nbt.SkipTag(r.Remaining())
		if err != nil {
			return true, err
		}
		r.Skip(n)
	}
	if err := packPacketEnd(r); err != nil {
		return true, err
	}
	if old := c.packs.jobs[id]; old != nil {
		old.cancel()
		delete(c.packs.jobs, id)
	}
	if !c.opts.HeadlessResourcePacks {
		return true, c.packStatus(id, packDeclined)
	}
	if err := packURL(address); err != nil {
		return true, c.packStatus(id, packInvalidURL)
	}
	// Bound outstanding downloads even when a server floods or replaces offers.
	// Completed workers release slots; replacement does not bypass the bound.
	if c.packs.jobs == nil {
		c.packs.jobs = make(map[protocol.UUID]*packJob)
	}
	if c.packs.active >= 4 {
		return true, c.packStatus(id, packDeclined)
	}
	if err := c.packStatus(id, packAccepted); err != nil {
		return true, err
	}
	workCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	job := &packJob{cancel: cancel}
	c.packs.jobs[id] = job
	c.packs.active++
	c.packs.wg.Add(1)
	go c.downloadResourcePack(ctx, workCtx, id, job, address, hash)
	return true, nil
}

// Called with a reserved worker slot; completion releases it under packs.mu.
func (c *Client) downloadResourcePack(ctx, workCtx context.Context, id protocol.UUID, job *packJob, address, hash string) {
	defer c.packs.wg.Done()
	defer job.cancel()
	status, err := validateResourcePack(workCtx, address, hash)
	c.packs.mu.Lock()
	defer c.packs.mu.Unlock()
	c.packs.active--
	if c.packs.closed || c.packs.jobs[id] != job {
		return
	}
	delete(c.packs.jobs, id)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		c.log.Warn("resource pack failed", "id", id.String(), "error", err)
	}
	if status == packLoaded || status == packFailedReload {
		if err = c.packStatus(id, packDownloaded); err != nil {
			_ = c.conn.Close()
			return
		}
		if status == packLoaded {
			c.log.Info("resource pack validated; simulating load without rendering", "id", id.String())
		}
	}
	if err = c.packStatus(id, status); err != nil {
		_ = c.conn.Close()
	}
}

func packPacketEnd(r *protocol.Reader) error {
	if err := r.Err(); err != nil {
		return err
	}
	if len(r.Remaining()) != 0 {
		return errors.New("understudy: trailing resource pack bytes")
	}
	return nil
}

func packURL(address string) error {
	u, err := url.Parse(address)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("resource pack URL must be HTTP(S), with a host and no credentials")
	}
	return nil
}

// Downloads go to a temporary file, never to a server-selected path. Validation
// checks the advertised digest, ZIP CRCs and basic metadata, not client asset
// compatibility. Nothing is extracted or retained after the simulated load.
func validateResourcePack(ctx context.Context, address, expected string) (int32, error) {
	if expected != "" {
		hash, err := hex.DecodeString(expected)
		if err != nil || len(hash) != sha1.Size {
			return packFailedDownload, errors.New("invalid resource pack SHA-1")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return packInvalidURL, err
	}
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many resource pack redirects")
		}
		return packURL(req.URL.String())
	}}
	resp, err := client.Do(req)
	if err != nil {
		return packFailedDownload, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return packFailedDownload, fmt.Errorf("resource pack HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > maxPackBytes {
		return packFailedDownload, errors.New("resource pack exceeds 64 MiB")
	}
	f, err := os.CreateTemp("", "understudy-pack-*.zip")
	if err != nil {
		return packFailedDownload, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	digest := sha1.New() // #nosec G401 -- Required by Minecraft, not used for authentication.
	size, err := io.Copy(io.MultiWriter(f, digest), io.LimitReader(resp.Body, maxPackBytes+1))
	if err != nil {
		return packFailedDownload, err
	}
	if size > maxPackBytes {
		return packFailedDownload, errors.New("resource pack exceeds 64 MiB")
	}
	if expected != "" && !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expected) {
		return packFailedDownload, errors.New("resource pack SHA-1 mismatch")
	}
	if err := validatePackArchive(ctx, f, size); err != nil {
		return packFailedReload, err
	}
	return packLoaded, nil
}

func validatePackArchive(ctx context.Context, f io.ReaderAt, size int64) error {
	z, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	if len(z.File) > 16384 {
		return errors.New("too many resource pack entries")
	}
	var expanded uint64
	metadata := false
	for _, entry := range z.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.UncompressedSize64 > 256<<20 || expanded+entry.UncompressedSize64 > 256<<20 {
			return errors.New("expanded resource pack exceeds 256 MiB")
		}
		expanded += entry.UncompressedSize64
		if entry.FileInfo().IsDir() {
			continue
		}
		src, err := entry.Open()
		if err != nil {
			return err
		}
		if entry.Name == "pack.mcmeta" {
			if metadata {
				_ = src.Close()
				return errors.New("duplicate pack.mcmeta")
			}
			var data []byte
			data, err = io.ReadAll(io.LimitReader(&packContextReader{ctx, src}, (1<<20)+1))
			if err == nil {
				var meta struct {
					Pack json.RawMessage `json:"pack"`
				}
				if len(data) > 1<<20 || json.Unmarshal(data, &meta) != nil || len(meta.Pack) == 0 || meta.Pack[0] != '{' {
					err = errors.New("invalid pack.mcmeta")
				}
			}
			metadata = true
		} else {
			_, err = io.Copy(io.Discard, &packContextReader{ctx, src})
		}
		_ = src.Close()
		if err != nil {
			return err
		}
	}
	if !metadata {
		return errors.New("missing pack.mcmeta")
	}
	return nil
}

type packContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *packContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
