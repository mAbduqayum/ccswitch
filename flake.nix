{
  description = "ccswitch — switch between Claude Code accounts";

  inputs.nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f:
        nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = self.shortRev or self.dirtyShortRev or "dev";
    in {
      packages = forAllSystems (pkgs: {
        default = pkgs.buildGoModule {
          pname = "ccswitch";
          inherit version;

          src = self;

          # After go.mod/go.sum change: set to nixpkgs.lib.fakeHash, run
          # `nix build`, and paste the real hash from the mismatch error.
          vendorHash = "sha256-Rrd3I5nrJDPyTYRz4VZ7+j1YmXT6Pv2lTlI/qPT0HmY=";

          subPackages = [ "cmd/ccswitch" ];

          env.CGO_ENABLED = 0;

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];

          nativeBuildInputs = [ pkgs.installShellFiles ];

          postInstall = nixpkgs.lib.optionalString
            (pkgs.stdenv.buildPlatform.canExecute pkgs.stdenv.hostPlatform) ''
              installShellCompletion --cmd ccswitch \
                --bash <($out/bin/ccswitch completions bash) \
                --zsh <($out/bin/ccswitch completions zsh) \
                --fish <($out/bin/ccswitch completions fish)
            '';

          meta = {
            description = "Switch between Claude Code accounts";
            homepage = "https://github.com/mAbduqayum/ccswitch";
            license = nixpkgs.lib.licenses.mit;
            mainProgram = "ccswitch";
          };
        };
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_25
            gopls
            golangci-lint
            gofumpt
            goreleaser
          ];
        };
      });
    };
}
