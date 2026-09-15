# Homebrew tap for turnrelay

`turnrelay.rb` is the formula that builds and installs the `turnrelay` CLI +
daemon from source. Homebrew-installed binaries are not quarantined, so the
(unsigned) binary runs without notarization or a Gatekeeper prompt.

## Publishing the tap (one time)

A Homebrew tap is a git repo named `homebrew-<name>`.

1. Push this repo to GitHub so the formula's source is reachable
   (`git push -u origin main`; the formula uses the `main` branch for `--HEAD`).
2. Create a repo `github.com/romanrublev/homebrew-turnrelay` with a `Formula/`
   directory and copy `turnrelay.rb` into it:

   ```
   homebrew-turnrelay/
     Formula/turnrelay.rb
   ```

3. For a stable (non-HEAD) release, tag the turnrelay repo and fill in the
   `url` + `sha256` lines noted at the top of the formula:

   ```
   git tag v0.3.0 && git push origin v0.3.0
   curl -sL https://github.com/romanrublev/turnrelay/archive/refs/tags/v0.3.0.tar.gz | shasum -a 256
   ```

## Installing

```
brew tap romanrublev/turnrelay
brew install --HEAD romanrublev/turnrelay/turnrelay   # or without --HEAD once a release is tagged
sudo turnrelay install                                # sets up the launchd/systemd service + socket owner
```

Then create the profile and use it (`turnrelay up` / `status` / `down`) as the
formula's caveats describe.

## Notes

- The formula installs only the `bin/turnrelay` binary. The privileged service
  is set up by `sudo turnrelay install` (which writes the owner-uid file that the
  daemon needs), not by `brew services` - `brew services` would start the daemon
  without that file. Use the bundled installer.
- The build fetches Go module dependencies. If Homebrew's build sandbox blocks
  network for module downloads on your setup, either vendor the app module
  (`cd app && go mod vendor`, commit `app/vendor/`) so the build is offline, or
  install with `--HEAD` after a `go mod download` warms the cache. This is the
  one step to verify on first real `brew install`.
- The GUI tray (`tray/` module, cgo) can ship as a second formula built with
  `cd tray && go build -o bin/turnrelay-tray .`; it is not included here yet.
