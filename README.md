# LazyGate

LazyGate is a Go proxy that keeps a Minecraft server reachable while its game process is asleep. It answers server-list requests and handles incoming connections while the backend starts.

The project began as a Go rewrite of [timvisee/lazymc](https://github.com/timvisee/lazymc) 0.2.11. My changes grew out of running Forge servers: online authentication, Velocity forwarding, and a mode where an external controller owns the game process.

## Features

- Hold, kick, forward, or experimental lobby behavior while a backend starts.
- Minecraft Java 1.20.1 / protocol 763 support, including Forge FML3 negotiation.
- Optional online authentication and Velocity modern forwarding.
- Cached Forge status metadata and bounded packet handling.
- Controller-managed mode, which leaves backend start/stop decisions to an external lifecycle helper.

## Build and run

Requires Go 1.23 or newer.

```bash
git clone https://github.com/swagsystems/lazygate.git
cd lazygate
go build -o lazygate .
./lazygate config generate
```

Edit the generated `lazymc.toml` for your server directory, launch command, and listening ports, then run `./lazygate start`. The configuration and internal module names retain `lazymc` for compatibility. The annotated [configuration template](res/lazymc.toml) documents the available settings.

For an external controller, enable `server.controller_managed`. The configured command becomes a lifecycle helper; backend socket checks determine readiness.

## Tests

```bash
go test -race ./...
go vet ./...
go build -o lazygate .
```

Tests exercise packet framing, mock-server handoffs, authentication helpers, and Forge negotiation. They do not establish compatibility with every Minecraft version or modpack. Lobby handoff remains experimental. The Go implementation has extensions and should not be treated as exact upstream parity.

## License and attribution

GPL-3.0; see [LICENSE](LICENSE) and [NOTICE.md](NOTICE.md). This is an independent derivative of lazymc, with no upstream endorsement. This repository contains source and tests; generated binaries and private deployment history are excluded.
