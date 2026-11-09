# AUR packaging

One split PKGBUILD that builds three packages from a tagged release:

| Package | Contents | Depends on |
|---|---|---|
| `protd` | daemon, `/usr/lib/systemd/user/protd.service` | none |
| `prot` | CLI and MCP server | `protd` |
| `prot-desktop` | Tauri app, desktop entry, icons | `prot`, `webkit2gtk-4.1`, `gtk3` |

Users enable the daemon with `systemctl --user enable --now protd`. `prot update` refuses to self-update
on these installs and points to the package manager.

## Releasing a new version

1. Merge to `main`, run the Bump Version workflow and let the `v<version>` tag build.
2. In this directory set `pkgver`, reset `pkgrel=1`, then run `updpkgsums` and
   `makepkg --printsrcinfo > .SRCINFO`.
3. Test with `makepkg -f` in a clean chroot (`extra-x86_64-build`).
4. Copy `PKGBUILD`, `.SRCINFO`, `protd.service` and `protd.install` into the AUR git repository
   (`ssh://aur@aur.archlinux.org/prot.git`) and push.

## Testing locally without a tag

Build from a tarball of your working tree:

```bash
git archive --prefix=prot-0.1.0/ HEAD | gzip > /tmp/prot-0.1.0.tar.gz
mkdir /tmp/aurtest && cp packaging/aur/{PKGBUILD,protd.service,protd.install} /tmp/aurtest/ && cd /tmp/aurtest
# point source=() at the local tarball, then:
makepkg -f
```

## Known warnings

`makepkg` reports "Package contains reference to $srcdir" for `prot-desktop`. Tauri embeds the crate's
manifest path at compile time; it is harmless.
