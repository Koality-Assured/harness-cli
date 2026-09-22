class Harness < Formula
  desc "Unified Harness CLI Control Plane for domain harnesses and spokes"
  homepage "https://github.com/Koality-Assured/harness-cli"
  version "0.3.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_darwin_arm64.tar.gz"
      sha256 "f461965f840e3d48157e002c6f540ae7e542b5aac4bc95ddc5ca065141a69592"
    else
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_darwin_amd64.tar.gz"
      sha256 "46cc86eb65eaa05b52438fdff732c2c3967a4113fcd517aafc5a8271c552873d"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_linux_arm64.tar.gz"
      sha256 "0e6cbfdf9bfb5b98483662b325a87634d0ea13dacab072feb6921642cfd156a5"
    else
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_linux_amd64.tar.gz"
      sha256 "8a5affda9ee3fd0c38d8c30a0127aeb48ab41585ca8135bfa3449bd82e9d9d02"
    end
  end

  def install
    bin.install "harness"
  end

  test do
    assert_match "Unified Harness CLI Control Plane", shell_output("#{bin}/harness --help")
  end
end
