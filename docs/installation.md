# Install Gong

Gong is a static binary. You can run it from the extracted directory or put it
on your PATH later.

## Contents

- [Download a release](#download-a-release)
- [Build it now](#build-it-now)
- [Put Gong on your PATH](#put-gong-on-your-path)
- [Keep one config for your user](#keep-one-config-for-your-user)
- [Build and check releases](#build-and-check-releases)

## Download a release

Choose the archive for your machine:

| System | Archive |
|---|---|
| Linux x86-64 | `gong_linux_amd64.tar.gz` |
| Linux ARM64 | `gong_linux_arm64.tar.gz` |
| macOS Intel | `gong_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `gong_darwin_arm64.tar.gz` |

Linux x86-64 example:

```sh
curl --fail --location --output gong_linux_amd64.tar.gz \
  https://github.com/creatorofuniverses/gong/releases/latest/download/gong_linux_amd64.tar.gz
curl --fail --location --output SHA256SUMS \
  https://github.com/creatorofuniverses/gong/releases/latest/download/SHA256SUMS
grep -F '  gong_linux_amd64.tar.gz' SHA256SUMS | sha256sum --check -
mkdir gong && tar -xzf gong_linux_amd64.tar.gz -C gong && cd gong
./gong version
```

On a Mac, use this instead (it selects Apple Silicon or Intel automatically):

```sh
case "$(uname -m)" in
  arm64) archive=gong_darwin_arm64.tar.gz ;;
  x86_64) archive=gong_darwin_amd64.tar.gz ;;
  *) echo "Unsupported Mac architecture" >&2; exit 1 ;;
esac
curl --fail --location --output "$archive" \
  "https://github.com/creatorofuniverses/gong/releases/latest/download/$archive"
curl --fail --location --output SHA256SUMS \
  https://github.com/creatorofuniverses/gong/releases/latest/download/SHA256SUMS
grep -F "  $archive" SHA256SUMS | shasum -a 256 --check -
mkdir gong && tar -xzf "$archive" -C gong && cd gong
./gong version
```

Archives and checksums are available on [GitHub Releases](https://github.com/creatorofuniverses/gong/releases/latest).
The repository is public; browser and curl downloads do not require authentication.

Do not use an archive if its checksum fails. Releases contain four stable
archive names, four versioned archive names, and one `SHA256SUMS` covering all
eight.

## Build it now

With the project's Go toolchain and [just](https://github.com/casey/just):

```sh
just build
bin/gong version
```

Use `bin/gong` in place of `./gong` in the guides.

## Put Gong on your PATH

This is optional:

```sh
install -d "$HOME/.local/bin"
install -m 0755 ./gong "$HOME/.local/bin/gong"
export PATH="$HOME/.local/bin:$PATH"
```

For future terminals, put the export in `~/.bashrc` for Bash or `~/.zshrc` for
Zsh. Fish uses `fish_add_path $HOME/.local/bin` in
`~/.config/fish/config.fish`.

## Keep one config for your user

Gong checks `./gong.yaml` first. For a config available from any directory, use
an absolute `XDG_CONFIG_HOME`, or the usual `$HOME/.config` fallback:

```sh
case "${XDG_CONFIG_HOME:-}" in
  /*) config_dir="$XDG_CONFIG_HOME/gong" ;;
  *) config_dir="$HOME/.config/gong" ;;
esac
install -d -m 0700 "$config_dir"
if [ ! -e "$config_dir/config.yaml" ]; then
  install -m 0600 gong.example.yaml "$config_dir/config.yaml"
fi
```

Edit the copy before starting Gong. The guard keeps an existing config intact.
See [Config discovery](configuration.md#config-discovery).

## Build and check releases

```sh
just release v0.1.0
just verify-release v0.1.0
```

Run these packaging commands on Linux; `just build` also works on macOS.
These commands build locally; they do not publish a GitHub release. The release
manifest includes the README, recipe index, and all nine focused guides.

Pushing a version tag such as `v0.1.0` starts the release workflow. It builds all
four archives, checks the macOS binaries on Intel and Apple Silicon runners,
and publishes the release only after both checks pass.

Project checks are `just test`, `just test-race`, `just vet`,
`just check-python`, and `just check-shell`.
