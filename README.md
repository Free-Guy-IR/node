# PasarGuard-Node
<p align="center">
    <a href="#">
        <img src="https://img.shields.io/github/actions/workflow/status/PasarGuard/node/docker-build.yml?style=flat-square" />
    </a>
    <a href="https://hub.docker.com/r/pasarguard/node" target="_blank">
        <img src="https://img.shields.io/docker/pulls/pasarguard/node?style=flat-square&logo=docker" />
    </a>
    <a href="#">
        <img src="https://img.shields.io/github/license/PasarGuard/node?style=flat-square" />
    </a>
    <a href="#">
        <img src="https://img.shields.io/github/stars/PasarGuard/node?style=social" />
    </a>
</p>

> Note: This is the [Free-Guy-IR](https://github.com/Free-Guy-IR) fork of the original [PasarGuard node](https://github.com/PasarGuard/node), extended with sing-box (Hysteria2), OpenVPN, MTProto (Telegram proxy), and L2TP/IPsec backend support.

# Documentation
You can find a full guide in docs https://docs.pasarguard.org/en/node/

# One-Click Installation (Recommended)
The easiest way to install PasarGuard Node is using our automated installation script:

```bash
sudo bash -c "$(curl -sL https://github.com/Free-Guy-IR/scripts/raw/main/pg-node.sh)" @ install
```

# Multiple Nodes on One Server

If you want to run several nodes on the same server, install each node with a unique name:

```bash
sudo bash -c "$(curl -sL https://github.com/Free-Guy-IR/scripts/raw/main/pg-node.sh)" @ install --name node-eu-1
```

After installation, use that same name as the prefix for management commands:

```bash
node-eu-1 update
node-eu-1 edit
node-eu-1 edit-env
```

You can run any other subcommand for that same node with the same prefix (`node-eu-1 ...`).

> ⚠️ **Important:** Never reuse ports between nodes. The connection port and every core's inbound/instance ports (Xray, sing-box, OpenVPN, MTProto, ...) attached to each node must be completely unique per node.

# L2TP/IPsec

The installer prepares this automatically: it loads and persists the `tun` and `ppp_generic` kernel modules,
persists `net.ipv4.ip_forward=1`, and maps `/dev/net/tun` and `/dev/ppp` into the container **only when the host
actually provides them**, so a host without PPP support still installs and runs every other core normally.

If PPP support is added to the host later (for example `apt install linux-modules-extra-$(uname -r)` on Ubuntu),
enable it without reinstalling:

```bash
pg-node l2tp-enable
```

Use the node's own name instead of `pg-node` for a named node (`node-eu-1 l2tp-enable`).

An L2TP node needs UDP **500**, **4500** and **1701** free on the host, and `network_mode: host` with
`NET_ADMIN`. Nothing else on the host may already run strongSwan/libreswan or xl2tpd, since the node starts its
own. A node running an L2TP core runs only that core.

For a manual (non-installer) deployment, add the override file to the compose command:

```bash
docker compose -f docker-compose.yml -f docker-compose.l2tp.yml up -d
```

# Donation
You can help PasarGuard team with your donations, [Click Here](https://donate.pasarguard.org/)

# Contributors

We ❤️‍🔥 contributors! If you'd like to contribute, please check out our [Contributing Guidelines](CONTRIBUTING.md) and feel free to submit a pull request or open an issue. We also welcome you to join our [Telegram](https://t.me/Pasar_Guard) group for either support or contributing guidance.

Check [open issues](https://github.com/PasarGuard/node/issues) to help the progress of this project.

## Stargazers over time
[![Stargazers over time](https://starchart.cc/PasarGuard/node.svg?variant=adaptive)](https://starchart.cc/PasarGuard/node)
                    
<p align="center">
Thanks to the all contributors who have helped improve PasarGuard Node:
</p>
<p align="center">
<a href="https://github.com/PasarGuard/node/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=PasarGuard/node" />
</a>
</p>
<p align="center">
  Made with <a rel="noopener noreferrer" target="_blank" href="https://contrib.rocks">contrib.rocks</a>
</p>
