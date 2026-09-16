class TurnrelayTray < Formula
  desc "System-tray GUI for the turnrelay VPN"
  homepage "https://github.com/romanrublev/turnrelay"
  version "0.4.0-beta"
  license "GPL-3.0-or-later"

  depends_on "romanrublev/turnrelay/turnrelay"

  on_macos do
    on_arm do
      url "https://github.com/romanrublev/turnrelay/releases/download/v0.4.0-beta/turnrelay-tray-darwin-arm64.tar.gz"
      sha256 "d9d30bf95165e71160f141bb60614fdb07202683b088e91b53ffc42401df5359"
    end
  end

  def install
    bin.install "turnrelay-tray"
  end

  def caveats
    <<~EOS
      Start the menu-bar app:

        turnrelay-tray &

      It drives the turnrelay CLI (Connect / Disconnect / status). Set up the
      daemon first with `sudo turnrelay install`. To run it at login, load the
      LaunchAgent at ~/Library/LaunchAgents/xyz.rublev.turnrelay-tray.plist.
    EOS
  end

  test do
    assert_path_exists bin/"turnrelay-tray"
  end
end
