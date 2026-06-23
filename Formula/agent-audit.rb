class AgentAudit < Formula
  desc "Local-first audit trails for AI coding agents"
  homepage "https://github.com/hfl0506/agent-audit"
  url "https://github.com/hfl0506/agent-audit/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "REPLACE_WITH_RELEASE_TARBALL_SHA256"
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/agent-audit"
  end

  test do
    system "#{bin}/agent-audit", "doctor"
  end
end
