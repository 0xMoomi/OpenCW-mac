# Third-party components

OpenCW links selected sing-box library modules rather than launching its CLI. Versions and integrity checks are pinned in `go.mod` / `go.sum`. The name does not imply endorsement by SagerNet or Nexon.

| Component | Use | License/source |
| --- | --- | --- |
| [sing-box](https://github.com/SagerNet/sing-box/tree/v1.14.3) | Routes, TUN, Hysteria2, Shadowsocks, VLESS, direct, local DNS | GPL-3.0-or-later; upstream also prohibits using its name or implying association for derivative works without consent |
| [sing-tun](https://github.com/SagerNet/sing-tun) | TUN (system stack), embedded Wintun | Pinned upstream license and driver notices |
| [Walk](https://github.com/lxn/walk), [Win](https://github.com/lxn/win) | Native Windows UI | BSD-style |
| [Go](https://go.dev/LICENSE) | Runtime and standard library | BSD-style |

The combined executable includes GPL-covered code. Redistribution must retain applicable notices and provide the corresponding source and build information under the relevant terms. Keep this source tree, dependency pins, build scripts, and complete notices with a redistribution. This repository does not grant a proprietary distribution exception. No relay profiles, TLS private keys, or directory snapshots are embedded in the executable.

Cloudflare trace and [public STUN](https://developers.cloudflare.com/realtime/turn/) endpoints are used for small exit and UDP checks. [IPWhois](https://ipwhois.io/documentation) is queried when UDP and HTTPS exit addresses differ. No JavaScript speed-test engine is copied into the app.

Subscription data is fetched in memory from [Au1rxx](https://github.com/Au1rxx/free-vpn-subscriptions), [morpheusadam](https://github.com/morpheusadam/v2ray-config), and [0xRadikal](https://github.com/0xRadikal/Free-v2ray-Configs). Their availability classifications are not treated as local UDP or game verification. No relay credentials are embedded in this repository or executable.
