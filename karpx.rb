class Karpx < Formula
  desc "⚡ Karpenter for EKS — managed from your terminal"
  homepage "https://github.com/kemilad/karpx"
  version "0.1.0"
  license "Apache-2.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/kemilad/karpx/releases/download/v#{version}/karpx_darwin_arm64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_DARWIN_ARM64"
    else
      url "https://github.com/kemilad/karpx/releases/download/v#{version}/karpx_darwin_amd64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_DARWIN_AMD64"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/kemilad/karpx/releases/download/v#{version}/karpx_linux_arm64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_LINUX_ARM64"
    else
      url "https://github.com/kemilad/karpx/releases/download/v#{version}/karpx_linux_amd64.tar.gz"
      sha256 "REPLACE_WITH_SHA256_LINUX_AMD64"
    end
  end

  def install
    bin.install "karpx"
  end

  def caveats
    <<~EOS
      Run `karpx` to open the interactive TUI.

      Requirements:
        - kubectl configured with your EKS cluster contexts
        - AWS credentials (env vars, ~/.aws/credentials, or IAM role)

      Quick start:
        karpx                          open TUI (current context)
        karpx -c my-cluster            target a specific cluster
        karpx -c my-cluster -r us-east-1
    EOS
  end

  test do
    assert_match "karpx", shell_output("#{bin}/karpx version")
  end
end
