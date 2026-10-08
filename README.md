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
   gh workflow run publish-release.yml --ref v0.6.21
   ```

The workflow refuses a branch, and a tag whose release is already published. It runs every gate on the tag's commit
and builds the binaries and the multi-arch image. Then one job checks that the draft exists and the tag has not moved,
uploads the zips and their `.sha256` files and tags the signed image `ghcr.io/free-guy-ir/node:<tag>`; if `:<tag>`
already holds another image it fails, because release image tags are never repointed. Right before publishing it reads
the tag and the draft again, and refuses when the tag moved or the release was published meanwhile; right after
publishing it reads the tag once more and turns the release back into a draft when the tag moved. Last, a job that only
one Publish Release run at a time may enter points `:latest` at the image of GitHub's latest release, so overlapping
runs and re-runs of older releases never move `:latest` backwards.

If a gate fails, the draft stays a draft without assets: re-run the workflow after a transient failure, otherwise fix
the cause on a new commit and release that under the next version. If a step fails after the image was tagged, re-run
that run's failed jobs, which reuse its image; a new run builds a new image and is refused.

## Release Guard

A release published by hand, or by any tool other than Publish Release, is turned back into a draft by the
**Release Guard** workflow, and that run fails. To publish it correctly, run the **Publish Release** workflow by
workflow_dispatch from the release tag:

```bash
gh workflow run publish-release.yml --ref vX.Y.Z
```

When a Publish Release run exists for that tag, the Release Guard message names it. Publish Release refuses to publish
a release that stopped being a draft during the run, so a human publish that lands before its last check fails the run;
re-run its failed jobs once the release is a draft again. A human publish that lands after that check is still turned
back into a draft by Release Guard while the run reports success; then re-run that run's publish job.

A release event runs the workflow files of the tagged commit, so Release Guard covers only tags whose commit
contains `.github/workflows/release-guard.yml`. Older tags and branches carry the old ungated `docker-build.yml`,
`docker-build-dev.yml` and `release.yml`, which publish images and move `:latest` without any gate. That is why the
publishing workflows live at new paths and the workflow policy refuses the old ones: the three old workflow ids are
meant to be disabled in the repository settings (Actions, the workflow, Disable workflow), which stops them on every
ref. Until the owner has disabled them, a release published on an old tag, or a manual run of the old workflow from
an old tag or branch, still publishes ungated. A tag ruleset that lets only admins create `v*` tags, and never move or
delete them, keeps new release tags on reviewed commits.

## Image tags

- `:<release tag>` and `:latest` are moved only by Publish Release. `:latest` points at the image of GitHub's latest
  release, the most recently published non-prerelease release. Release image tags are immutable.
- `:main`, `:dev` and the other branch tags are moved by the branch build on every push, and only while the branch
  still points at the built commit.
- **Build and Push Docker Image (Manual)** rebuilds an image by hand and never moves `:latest`. From `main` it moves
  `:edge`, and it can add a custom tag. From another branch it publishes only the branch-named tag. From a tag it
  needs a custom tag. A custom tag may not be `latest`, `edge` or `main`, may not start with `v` and a digit or with
  `sha256-` (those names hold the image signatures and attestations), and may not be the name of an existing release,
  tag or branch.

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
