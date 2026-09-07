# Horizon LazyMC gateway notices

This directory contains a Go rewrite of
[`timvisee/lazymc`](https://github.com/timvisee/lazymc), based on the behavior
of the upstream `0.2.11` release. The component is distributed under the GNU
General Public License version 3; see `LICENSE`. It is a separately operated
gateway process and is not relicensed under Horizon's root MIT license.

The Go rewrite and subsequent modifications add Minecraft Java online authentication, Velocity modern
profile forwarding, Forge 1.20.1/FML3 negotiation and cached replay, bounded
public-edge framing and admission, persisted Forge status metadata, and a
fixed capability-helper lifecycle mode in which Horizon remains the sole game
process owner.

These modifications are not endorsed by or affiliated with the upstream
project. Release artifacts must include this notice, the GPL license, and the
matching corresponding source. A reproducible binary can be built from the
component source with:

```bash
CGO_ENABLED=0 go build -trimpath -buildvcs=false -o horizon-lazymc .
```
