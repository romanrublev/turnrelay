# Homebrew tap for turnrelay

`turnrelay.rb` is the formula. On macOS Apple Silicon it installs a prebuilt
binary attached to a GitHub release (no build); other platforms and
`--HEAD` build from source.

The tap repo lives at https://github.com/romanrublev/homebrew-turnrelay
(`Formula/turnrelay.rb` there is a copy of this file).

## Installing

```
brew tap romanrublev/turnrelay
brew install romanrublev/turnrelay/turnrelay      # macOS arm64: pours prebuilt binary
sudo turnrelay install                            # sets up the launchd/systemd service + socket owner
```

Source build (other platforms, or latest main) needs the sandbox off because the
build fetches Go modules:

```
HOMEBREW_NO_SANDBOX=1 brew install --HEAD romanrublev/turnrelay/turnrelay
```

## Cutting a new prebuilt release

The prebuilt binary avoids the sandbox/build entirely for the common case. To
publish a new version:

```
# 1. Build the release binary
cd app && go build -tags "with_wireguard,with_gvisor,with_quic" -trimpath -o /tmp/turnrelay .

# 2. Tar it and get the sha256
cd /tmp && tar -czf turnrelay-darwin-arm64.tar.gz turnrelay && shasum -a 256 turnrelay-darwin-arm64.tar.gz

# 3. Tag and release
git tag -a vX.Y.Z -m "..." && git push origin vX.Y.Z
gh release create vX.Y.Z /tmp/turnrelay-darwin-arm64.tar.gz --repo romanrublev/turnrelay

# 4. Update turnrelay.rb: bump `version`, and the on_arm `url` + `sha256`.
#    Copy it into the tap's Formula/ and push. `brew style` must be clean.
```

## Notes

- The formula installs only the `bin/turnrelay` binary. The privileged service
  is set up by `sudo turnrelay install`, not by `brew services` (`brew services`
  would start the daemon without the owner-uid file that `install` writes).
- Prebuilt binaries are currently macOS arm64 only. Add `on_intel` / `on_linux`
  url blocks (with their own release assets) to cover more platforms; until then
  those platforms use the `--HEAD` source build.
- A formal Homebrew `bottle do` block (built with `brew bottle`) is an
  alternative to the release-asset approach; the release asset is simpler for a
  personal tap and portable across platforms.
- The GUI tray (`tray/` module, cgo) can ship as a second formula built with
  `cd tray && go build -o bin/turnrelay-tray .`; it is not included here yet.
