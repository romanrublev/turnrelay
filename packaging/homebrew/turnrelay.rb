class Turnrelay < Formula
  desc "Tunnel UDP through WebRTC TURN relays as a system VPN"
  homepage "https://github.com/romanrublev/turnrelay"
  license "GPL-3.0-or-later"
  head "https://github.com/romanrublev/turnrelay.git", branch: "main"

  # For a tagged release, replace `head` above (or add alongside) with:
  #   url "https://github.com/romanrublev/turnrelay/archive/refs/tags/v0.3.0.tar.gz"
  #   sha256 "<shasum -a 256 of the tarball>"

  depends_on "go" => :build

  def install
    # The command lives in the nested app/ module; its go.mod replaces the core
    # and singbox modules with ../ and ../singbox, which resolve inside the
    # checked-out repo. Build with the transport tags the engine needs.
    cd "app" do
      system "go", "build",
             "-tags", "with_wireguard,with_gvisor,with_quic",
             "-trimpath", "-o", bin/"turnrelay", "."
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
