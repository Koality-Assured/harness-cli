class Harness < Formula
  desc "Unified Harness CLI Control Plane for domain harnesses and spokes"
  homepage "https://github.com/Koality-Assured/harness-cli"
  version "0.1.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_darwin_arm64.tar.gz"
      sha256 "8bfbf3f860f191e24fdd1b3b9986907b1ce4eaf8792906276cb48324f899ecf7"
    else
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_darwin_amd64.tar.gz"
      sha256 "d11748d271e070b823bdd799bbfa5b9a7f8e3658cd6307b0ba205d9b40cbf9f6"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_linux_arm64.tar.gz"
      sha256 "3d4b766084c9fcd96f421f92efce861d2d1d1d9769a074f9cd508a50fc507f52"
    else
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_linux_amd64.tar.gz"
      sha256 "5cb9ccc14f17928f8b37d65e021a4e8357546bcd2397b694e9f5f73641787dcf"
    end
  end

  def install
    bin.install "harness"
  end

  test do
    assert_match "Unified Harness CLI Control Plane", shell_output("#{bin}/harness --help")
  end
end
