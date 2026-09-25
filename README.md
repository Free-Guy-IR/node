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

> Note: This is the [Free-Guy-IR](https://github.com/Free-Guy-IR) fork of the original [PasarGuard node](https://github.com/Free-Guy-IR/node), extended with sing-box (Hysteria2), OpenVPN, MTProto (Telegram proxy), and L2TP/IPsec backend support.

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

# Moving a Node to Another Panel

Restart the node container before you connect it to a different panel:

```bash
node-eu-1 restart
```

Within one panel, the node carries traffic that the panel has not read yet across a core restart, so no byte is lost or billed twice. A different panel would receive that carried traffic and bill it to its own users with the same numeric ids. Restarting the container first discards it.

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

# Releasing

Releases are published by CI, never by hand. A release gets its binaries and its image only after every gate passes on
the release commit: the test suite, the real-kernel WireGuard tests, the MTProto fronting leg, govulncheck, the workflow
policy check and the upstream drift check.

1. Tag the release commit and push the tag. Never move or reuse a tag that was already released.

   ```bash
   git tag v0.6.21 <commit>
   git push origin v0.6.21
   ```

2. On GitHub, draft a release on that existing tag: write the notes, tick "Set as a pre-release" if it is one, and
   choose **Save draft**. Do not publish it.

3. Run the **Publish Release** workflow from the tag: Actions, Publish Release, Run workflow, and under "Use workflow
   from" pick the tag. Or:

   ```bash
   gh workflow run release.yml --ref v0.6.21
   ```

The workflow refuses a branch, and a tag whose release is already published. It runs every gate on the tag's commit
and builds the binaries and the multi-arch image. Then one job checks that the draft exists and the tag has not moved,
uploads the zips and their `.sha256` files, tags the signed image `ghcr.io/free-guy-ir/node:<tag>` (and `:latest`
unless the draft is a pre-release), and publishes the draft. If a gate fails, the draft stays a draft without assets:
re-run the workflow after a transient failure, otherwise fix the cause on a new commit and release that under the next
version.

A release published by hand, or by any tool other than Publish Release, is turned back into a draft by the
**Release Guard** workflow, which fails with a note saying so. Run Publish Release for it instead.

**Build and Push Docker Image (Manual)** rebuilds an image by hand. It adds `:latest` only when run from `main` or from
a tag with a published, non-prerelease release. From any other ref it publishes only the ref-named tag.

# Contributors

We ❤️‍🔥 contributors! If you'd like to contribute, please check out our [Contributing Guidelines](CONTRIBUTING.md) and feel free to submit a pull request or open an issue. We also welcome you to join our [Telegram](https://t.me/Pasar_Guard) group for either support or contributing guidance.

Check [open issues](https://github.com/Free-Guy-IR/node/issues) to help the progress of this project.

## Stargazers over time
[![Stargazers over time](https://starchart.cc/Free-Guy-IR/node.svg?variant=adaptive)](https://starchart.cc/Free-Guy-IR/node)
                    
<p align="center">
Thanks to the all contributors who have helped improve PasarGuard Node:
</p>
<p align="center">
<a href="https://github.com/Free-Guy-IR/node/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=PasarGuard/node" />
</a>
</p>
<p align="center">
  Made with <a rel="noopener noreferrer" target="_blank" href="https://contrib.rocks">contrib.rocks</a>
</p>
