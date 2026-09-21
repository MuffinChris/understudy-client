---
title: Resource packs
nav_order: 6
---

The Rift fork can exercise Java server resource-pack admission with an explicit
headless mode:

```sh
./understudy-client -addr 127.0.0.1:25565 -username RiftTest_Bot \
  -headless-resource-packs -hold 0
```

Use an address and identity admitted by your test server. The option works the
same for a local bot and a bot running on a remote tester's machine. It does
not configure server access. The remote-control HTTP API is a separate option.

Without `-headless-resource-packs`, offers are declined, including required
packs. A server may disconnect a client that declines a required pack. Library
users opt in with `Options{HeadlessResourcePacks: true}`.

In headless mode the client accepts the offer, downloads it, checks SHA-1 when
provided, validates ZIP entries and their CRCs, and requires a root
`pack.mcmeta` JSON object containing a `pack` object. It then sends `DOWNLOADED`
and `SUCCESSFULLY_LOADED`. The latter is **simulated application**: assets are
not rendered, version compatibility is not established, and this cannot prove
that fonts, models, textures, sounds or UI look correct in Minecraft. Successful
validation and simulated application are identified explicitly in the log.

Only use this option with trusted servers: they choose HTTP(S) download URLs,
including addresses reachable from the bot's machine. URLs with credentials and
non-HTTP schemes are rejected. There are at most four simultaneous downloads,
each bounded to 60 seconds, 64 MiB downloaded, 256 MiB expanded, 16,384 ZIP
entries and 1 MiB of metadata. Redirects are checked and bounded. The client
declines excess offers. Downloaded files are temporary, never extracted, and
removed after validation or failure; there is no cache or persistent pack stack.

The packet reader remains available during downloads. Configuration and play
both support offers and removals; completion replies use the current phase.
Removing or replacing an offer cancels its download and suppresses stale replies.
Disconnect, `Run` exit and `Close` cancel remaining work. Use a context that
remains alive for the session when calling `Connect`.

HTTP, size and digest failures report `FAILED_DOWNLOAD`. Invalid ZIPs or metadata
report `DOWNLOADED` followed by `FAILED_RELOAD`. Invalid initial URLs report
`INVALID_URL`. Failed validation never reports a successful load.

## Protocol evidence and checks

The packet IDs and UUID/string/optional-NBT layouts come from
[minecraft-data at f59168c](https://github.com/PrismarineJS/minecraft-data/tree/f59168c3ce23158527ea7c3743ac16f18f96cf25/data/pc).
The 26.2 table follows the repository's verified unchanged 26.1 packet grammar
in `internal/gen/reports-to-mcdata.mjs`. The action enum order was additionally
checked against the Mojang 26.2 client class
`net.minecraft.network.protocol.common.ServerboundResourcePackPacket.Action`.

To refresh just packet IDs while preserving independently measured world tables:

```sh
node internal/gen/genversion.mjs /path/to/minecraft-data/data \
  26.1 protocol/versions/version_26_1.go --packets-only
gofmt -w protocol/versions/version_26_1.go
```

Tests use local HTTP servers and framed Minecraft connections to check status
order, both protocol phases, version-specific IDs, malformed offers, default
declines, validation failures, download bounds, replacement and cancellation.
They are protocol fixtures, not live-server or visual acceptance evidence.

## Bedrock

This implementation speaks Java Edition. A Bedrock bot needs a separate client
such as [Gophertunnel](https://github.com/Sandertv/gophertunnel), entering the
server through its Bedrock/Geyser endpoint. That would test translation,
Bedrock inventory/input behavior and Bedrock resource-pack negotiation.
Bedrock authentication and packs need their own implementation; Java offline
admission and Java pack acknowledgements do not provide them. Real Bedrock
clients remain necessary for visual and device-specific testing.
