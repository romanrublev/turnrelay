class Turnrelay < Formula
  desc "Tunnel UDP through WebRTC TURN relays as a system VPN"
  homepage "https://github.com/romanrublev/turnrelay"
  version "0.3.0-beta"
  license "GPL-3.0-or-later"

  # Prebuilt binary for Apple Silicon: `brew install` pours it, no build and no
  # sandbox to disable. Other platforms (and `brew install --HEAD`) build from
  # source, which fetches Go modules and so needs `HOMEBREW_NO_SANDBOX=1`.
  head "https://github.com/romanrublev/turnrelay.git", branch: "main"

  depends_on "go" => :build

  on_macos do
    on_arm do
      url "https://github.com/romanrublev/turnrelay/releases/download/v0.3.0-beta/turnrelay-darwin-arm64.tar.gz"
      sha256 "ad2a7a7b723ff5a1a0f74d413eba8e46264051485171d432e0d961b79d48594d"
    end
  end

  def install
    if build.head?
      cd "app" do
        system "go", "build",
               "-tags", "with_wireguard,with_gvisor,with_quic",
               "-trimpath", "-o", bin/"turnrelay", "."
      end
    else
      bin.install "turnrelay"
    end
  end

  def caveats
    <<~EOS
      turnrelay needs a privileged daemon (it creates a tun interface). Install
      and start it with the bundled installer, which sets up launchd (macOS) or
      systemd (Linux) and records your user as the socket owner:

        sudo turnrelay install

      Then write your profile (chmod 600) and connect:

        ~/Library/Application Support/turnrelay/profile.json   (macOS)
        ~/.config/turnrelay/profile.json                       (Linux)

        turnrelay up
        turnrelay status
        turnrelay down

      Profile fields (link, server, connections, mode, wg_private_key,
      wg_peer_public_key, wg_address, optional log_level): see #{homepage}.

      To remove the service: sudo turnrelay uninstall
    EOS
  end

  test do
    output = shell_output("#{bin}/turnrelay 2>&1", 2)
    assert_match "usage", output
  end
end
