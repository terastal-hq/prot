# AUR packaging

Three packages from two AUR package bases, one directory each:

| Directory (AUR base) | Package | Contents | Depends on | Builds with |
|---|---|---|---|---|
| `prot/` | `protd` | daemon, `/usr/lib/systemd/user/protd.service` | none | `go` |
| `prot/` | `prot` | CLI, MCP server, shell completions | `protd` | `go` |
| `prot-desktop/` | `prot-desktop` | Tauri app, desktop entry, icons | `prot`, `webkit2gtk-4.1`, `gtk3` | `go`, `rust`, `nodejs`, `npm` |

They are separate bases on purpose. makepkg runs `build()` once per base, whichever package of it you asked for, so
when the desktop app shared a PKGBUILD with the CLI, `yay -S prot` compiled the app and pulled in its toolchains.

Users enable the daemon with `systemctl --user enable --now protd`. `prot update` refuses to self-update
on these installs and points to the package manager.

## Releasing a new version

Pushing a `v*` tag runs the Publish to AUR workflow, which sets `pkgver`, fills in the source checksum and pushes both
bases, `prot` first and then `prot-desktop` (the AUR has to see `prot-desktop` leave the `prot` base before it can
become a base of its own). There is no `.SRCINFO` in this repository: the deploy action generates it from the PKGBUILD.

To do it by hand, in each directory set `pkgver`, reset `pkgrel=1`, run `updpkgsums` and `makepkg --printsrcinfo >
.SRCINFO`, test with `makepkg -f` in a clean chroot (`extra-x86_64-build`), then copy the files (`PKGBUILD`, `.SRCINFO`,
and for `prot/` also `protd.service` and `protd.install`) into the matching AUR git repository
(`ssh://aur@aur.archlinux.org/<base>.git`) and push.

## Testing locally without a tag

Build from a tarball of your working tree. Point `source=()` at it and skip the checksum:

```bash
git archive --prefix=prot-0.2.1/ HEAD | gzip > /tmp/src.tar.gz
mkdir -p /tmp/aurtest && cp packaging/aur/prot/{PKGBUILD,protd.service,protd.install} /tmp/aurtest/ && cd /tmp/aurtest
# change the first source=() entry to "prot-$pkgver.tar.gz::file:///tmp/src.tar.gz", then:
makepkg -f --skipchecksums
```

For `prot-desktop/` do the same with `"$pkgname-$pkgver.tar.gz::file:///tmp/src.tar.gz"`.

## Known warnings

`makepkg` reports "Package contains reference to $srcdir" for `prot-desktop`. Tauri embeds the crate's
manifest path at compile time; it is harmless.
