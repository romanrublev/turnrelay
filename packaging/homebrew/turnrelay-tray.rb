class TurnrelayTray < Formula
  desc "System-tray GUI for the turnrelay VPN"
  homepage "https://github.com/romanrublev/turnrelay"
  version "0.7.0-beta"
  license "GPL-3.0-or-later"

  depends_on "romanrublev/turnrelay/turnrelay"

  on_macos do
    on_arm do
      url "https://github.com/romanrublev/turnrelay/releases/download/v0.7.0-beta/turnrelay-tray-darwin-arm64.tar.gz"
      sha256 "1957795ef8a0504b2f323491e1b0532fa72bfb92b0dc596ad9853604160a921f"
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
