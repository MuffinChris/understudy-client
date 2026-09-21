package understudy

import (
	"context"
	"strings"
	"testing"

	"github.com/blocktopiaworld/understudy-client/protocol"
)

func TestConfigurationDisconnectNBT(t *testing.T) {
	// Anonymous NBT string and compound fixtures, matching configuration's
	// packet_disconnect reason grammar in minecraft-data, rather than a String.
	for _, data := range [][]byte{
		append([]byte{8, 0, 13}, []byte("Access denied")...),
		append(append([]byte{10, 8, 0, 4, 't', 'e', 'x', 't', 0, 13}, []byte("Access denied")...), 0),
	} {
		c, _ := newSession(t)
		defer c.Close()
		_, err := c.handleConfigPacket(context.Background(), protocol.Packet{ID: c.v.Packets.CBConfigDisconnect, Data: data})
		if err == nil || !strings.Contains(err.Error(), "disconnected during configuration") || !strings.Contains(err.Error(), "Access denied") {
			t.Fatalf("wrong reason: %v", err)
		}
		for _, bad := range [][]byte{data[:len(data)-1], append(append([]byte{}, data...), 1)} {
			_, err = c.handleConfigPacket(context.Background(), protocol.Packet{ID: c.v.Packets.CBConfigDisconnect, Data: bad})
			if err == nil || strings.Contains(err.Error(), "disconnected during configuration:") {
				t.Fatalf("malformed payload reported as valid: %v", err)
			}
		}
	}
}
