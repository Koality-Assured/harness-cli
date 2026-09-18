class Harness < Formula
  desc "Unified Harness CLI Control Plane for domain harnesses and spokes"
  homepage "https://github.com/Koality-Assured/harness-cli"
  version "0.1.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_darwin_arm64.tar.gz"
      sha256 "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    else
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_darwin_amd64.tar.gz"
      sha256 "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_linux_arm64.tar.gz"
      sha256 "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    else
      url "https://github.com/Koality-Assured/harness-cli/releases/download/v#{version}/harness_#{version}_linux_amd64.tar.gz"
      sha256 "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    end
  end

  def install
    bin.install "harness"
  end

  test do
    assert_match "Unified Harness CLI Control Plane", shell_output("#{bin}/harness --help")
  end
end
