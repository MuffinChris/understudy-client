package understudy

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blocktopiaworld/understudy-client/protocol"
)

func testPack(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte(`{"pack":{"pack_format":75,"description":"fixture"}}`)); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func packOffer(c *Client, id protocol.UUID, address, hash string, config bool) protocol.Packet {
	packetID := c.v.Packets.CBPlayResourcePackPush
	if config {
		packetID = c.v.Packets.CBConfigResourcePackPush
	}
	w := protocol.NewWriter(packetID).UUID(id).String(address).String(hash).Bool(true).Bool(false)
	r := protocol.NewReader(w.Bytes())
	got := r.VarInt()
	return protocol.Packet{ID: got, Data: r.Remaining()}
}

func waitPackPackets(t *testing.T, s *fakeServer, n int) []protocol.Packet {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if packets := s.received(); len(packets) >= n {
			return packets
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("got %d packets, want %d", len(s.received()), n)
	return nil
}

func checkPackStatus(t *testing.T, p protocol.Packet, packetID int32, id protocol.UUID, status int32) {
	t.Helper()
	r := p.Reader()
	if p.ID != packetID || r.UUID() != id || r.VarInt() != status || r.Err() != nil || len(r.Remaining()) != 0 {
		t.Fatalf("wrong resource pack response: %+v; want packet=%d status=%d", p, packetID, status)
	}
}

func TestResourcePackSuccessAcrossVersionsAndStates(t *testing.T) {
	data := testPack(t, "pack.mcmeta")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	defer server.Close()
	for _, version := range protocol.Names() {
		for _, config := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/config=%v", version, config), func(t *testing.T) {
				c, s := newSession(t)
				t.Cleanup(func() { _ = c.Close() })
				c.v, _ = protocol.ByName(version)
				c.opts.HeadlessResourcePacks = true
				packetID := c.v.Packets.SBPlayResourcePack
				if config {
					c.setState(protocol.StateConfiguration)
					packetID = c.v.Packets.SBConfigResourcePack
				}
				id := protocol.OfflineUUID("pack")
				p := packOffer(c, id, server.URL, fmt.Sprintf("%x", sha1.Sum(data)), config)
				if config {
					_, err := c.handleConfigPacket(context.Background(), p)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					_, err := c.dispatch(context.Background(), p)
					if err != nil {
						t.Fatal(err)
					}
				}
				packets := waitPackPackets(t, s, 3)
				for i, status := range []int32{packAccepted, packDownloaded, packLoaded} {
					checkPackStatus(t, packets[i], packetID, id, status)
				}
			})
		}
	}
}

func TestResourcePackFailures(t *testing.T) {
	valid := testPack(t, "pack.mcmeta")
	missing := testPack(t, "other.txt")
	for _, tc := range []struct {
		name string
		data []byte
		hash string
		code int
		want int32
	}{
		{"hash", valid, strings.Repeat("0", 40), 200, packFailedDownload},
		{"bad hash", valid, "bad", 200, packFailedDownload},
		{"HTTP", valid, "", 404, packFailedDownload},
		{"not zip", []byte("bad"), "", 200, packFailedReload},
		{"missing metadata", missing, "", 200, packFailedReload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code); _, _ = w.Write(tc.data) }))
			defer server.Close()
			status, err := validateResourcePack(context.Background(), server.URL, tc.hash)
			if status != tc.want || err == nil {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
}

func TestResourcePackDefaultDeclineAndInvalidURL(t *testing.T) {
	c, s := newSession(t)
	defer c.Close()
	id := protocol.OfflineUUID("pack")
	_, err := c.handleResourcePack(context.Background(), packOffer(c, id, "https://example.invalid/pack", "", false), false)
	if err != nil {
		t.Fatal(err)
	}
	checkPackStatus(t, waitPackPackets(t, s, 1)[0], c.v.Packets.SBPlayResourcePack, id, packDeclined)
	c.opts.HeadlessResourcePacks = true
	_, err = c.handleResourcePack(context.Background(), packOffer(c, id, "file:///tmp/pack", "", false), false)
	if err != nil {
		t.Fatal(err)
	}
	checkPackStatus(t, waitPackPackets(t, s, 2)[1], c.v.Packets.SBPlayResourcePack, id, packInvalidURL)
}

func TestResourcePackTransitionKeepsReaderResponsive(t *testing.T) {
	data := testPack(t, "pack.mcmeta")
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			_, _ = w.Write(data)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	c, s := newSession(t)
	defer c.Close()
	c.opts.HeadlessResourcePacks = true
	c.setState(protocol.StateConfiguration)
	id := protocol.OfflineUUID("pack")
	if _, err := c.handleConfigPacket(context.Background(), packOffer(c, id, server.URL, "", true)); err != nil {
		t.Fatal(err)
	}
	keep := protocol.NewWriter(0).I64(42).Bytes()[1:]
	if _, err := c.handleConfigPacket(context.Background(), protocol.Packet{ID: c.v.Packets.CBConfigKeepAlive, Data: keep}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.handleConfigPacket(context.Background(), protocol.Packet{ID: c.v.Packets.CBConfigFinishConfiguration}); err != nil {
		t.Fatal(err)
	}
	packets := waitPackPackets(t, s, 3)
	if packets[1].ID != c.v.Packets.SBConfigKeepAlive || packets[2].ID != c.v.Packets.SBConfigFinishConfiguration {
		t.Fatal("read loop did not progress")
	}
	close(release)
	packets = waitPackPackets(t, s, 5)
	checkPackStatus(t, packets[3], c.v.Packets.SBPlayResourcePack, id, packDownloaded)
	checkPackStatus(t, packets[4], c.v.Packets.SBPlayResourcePack, id, packLoaded)
}

func TestResourcePackPopAndCloseCancelDownload(t *testing.T) {
	for _, pop := range []bool{false, true} {
		t.Run(fmt.Sprint(pop), func(t *testing.T) {
			started := make(chan struct{})
			cancelled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(cancelled) }))
			defer server.Close()
			c, s := newSession(t)
			defer c.Close()
			c.opts.HeadlessResourcePacks = true
			id := protocol.OfflineUUID("pack")
			if _, err := c.handleResourcePack(context.Background(), packOffer(c, id, server.URL, "", false), false); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("download not started")
			}
			if pop {
				if _, err := c.handleResourcePack(context.Background(), protocol.Packet{ID: c.v.Packets.CBPlayResourcePackPop, Data: []byte{0}}, false); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = c.Close()
			}
			select {
			case <-cancelled:
			case <-time.After(3 * time.Second):
				t.Fatal("download not cancelled")
			}
			c.packs.wg.Wait()
			if len(waitPackPackets(t, s, 1)) != 1 {
				t.Fatal("cancelled pack sent terminal response")
			}
		})
	}
}

func TestResourcePackMalformedOffer(t *testing.T) {
	c, s := newSession(t)
	defer c.Close()
	p := packOffer(c, protocol.OfflineUUID("pack"), "https://example.invalid", "", false)
	for i := 0; i < len(p.Data); i++ {
		bad := p
		bad.Data = p.Data[:i]
		if _, err := c.handleResourcePack(context.Background(), bad, false); err == nil {
			t.Fatalf("accepted truncated length %d", i)
		}
	}
	if len(s.received()) != 0 {
		t.Fatal("malformed packet emitted response")
	}
}

func TestResourcePackPromptAndTargetedPop(t *testing.T) {
	c, s := newSession(t)
	defer c.Close()
	id := protocol.OfflineUUID("pack")
	// A nameless TAG_String prompt, as specified by anonymousNbt. The preceding
	// false optional marker becomes true. This covers the variable-width branch.
	p := packOffer(c, id, "https://example.invalid/pack", "", false)
	p.Data[len(p.Data)-1] = 1
	p.Data = append(p.Data, 8, 0, 2, 'h', 'i')
	if _, err := c.handleResourcePack(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	checkPackStatus(t, waitPackPackets(t, s, 1)[0], c.v.Packets.SBPlayResourcePack, id, packDeclined)
	p.Data = p.Data[:len(p.Data)-1]
	if _, err := c.handleResourcePack(context.Background(), p, false); err == nil {
		t.Fatal("accepted truncated prompt")
	}
	aCancelled, bCancelled := false, false
	other := protocol.OfflineUUID("other")
	c.packs.jobs = map[protocol.UUID]*packJob{id: {cancel: func() { aCancelled = true }}, other: {cancel: func() { bCancelled = true }}}
	payload := append([]byte{1}, id[:]...)
	if _, err := c.handleResourcePack(context.Background(), protocol.Packet{ID: c.v.Packets.CBPlayResourcePackPop, Data: payload}, false); err != nil {
		t.Fatal(err)
	}
	if !aCancelled || bCancelled || len(c.packs.jobs) != 1 {
		t.Fatal("targeted removal affected wrong pack")
	}
}

func TestResourcePackReplacementSuppressesStaleCompletion(t *testing.T) {
	data := testPack(t, "pack.mcmeta")
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			close(started)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	c, s := newSession(t)
	defer c.Close()
	c.opts.HeadlessResourcePacks = true
	id := protocol.OfflineUUID("pack")
	if _, err := c.handleResourcePack(context.Background(), packOffer(c, id, server.URL+"/old", "", false), false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("old download not started")
	}
	if _, err := c.handleResourcePack(context.Background(), packOffer(c, id, server.URL+"/new", "", false), false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("old download not cancelled")
	}
	c.packs.wg.Wait()
	packets := waitPackPackets(t, s, 4)
	if len(packets) != 4 {
		t.Fatal("stale completion sent a status")
	}
	for i, status := range []int32{packAccepted, packAccepted, packDownloaded, packLoaded} {
		checkPackStatus(t, packets[i], c.v.Packets.SBPlayResourcePack, id, status)
	}
}

func TestResourcePackDownloadBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	c, s := newSession(t)
	defer c.Close()
	c.opts.HeadlessResourcePacks = true
	for i := 0; i < 5; i++ {
		id := protocol.OfflineUUID(fmt.Sprint(i))
		if _, err := c.handleResourcePack(context.Background(), packOffer(c, id, server.URL, "", false), false); err != nil {
			t.Fatal(err)
		}
	}
	packets := waitPackPackets(t, s, 5)
	checkPackStatus(t, packets[4], c.v.Packets.SBPlayResourcePack, protocol.OfflineUUID("4"), packDeclined)
}

func TestResourcePackOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxPackBytes+1))
	}))
	defer server.Close()
	status, err := validateResourcePack(context.Background(), server.URL, "")
	if status != packFailedDownload || err == nil {
		t.Fatalf("status=%d err=%v", status, err)
	}
}

func TestResourcePackArchiveIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name, metadata     string
		duplicate, corrupt bool
	}{
		{name: "invalid JSON", metadata: "{"},
		{name: "missing pack", metadata: `{"description":"test"}`},
		{name: "duplicate metadata", metadata: `{"pack":{}}`, duplicate: true},
		{name: "CRC mismatch", metadata: `{"pack":{}}`, corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			n := 1
			if tc.duplicate {
				n = 2
			}
			for range n {
				f, err := z.CreateHeader(&zip.FileHeader{Name: "pack.mcmeta", Method: zip.Store})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.Write([]byte(tc.metadata)); err != nil {
					t.Fatal(err)
				}
			}
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			data := b.Bytes()
			if tc.corrupt {
				i := bytes.Index(data, []byte(tc.metadata))
				data[i+2] = 'b'
			}
			if err := validatePackArchive(context.Background(), bytes.NewReader(data), int64(len(data))); err == nil {
				t.Fatal("accepted invalid archive")
			}
		})
	}
}

func TestResourcePackFailedReloadWireStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not a zip")) }))
	defer server.Close()
	c, s := newSession(t)
	defer c.Close()
	c.opts.HeadlessResourcePacks = true
	id := protocol.OfflineUUID("pack")
	if _, err := c.handleResourcePack(context.Background(), packOffer(c, id, server.URL, "", false), false); err != nil {
		t.Fatal(err)
	}
	packets := waitPackPackets(t, s, 3)
	for i, status := range []int32{3, 4, 6} {
		checkPackStatus(t, packets[i], c.v.Packets.SBPlayResourcePack, id, status)
	}
}
