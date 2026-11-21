{ inputs, ... }:
{
  imports = [
    inputs.treefmt-nix.flakeModule
  ];

  perSystem =
    { pkgs, ... }:
    {
      treefmt = {
        projectRootFile = "flake.nix";
        programs = {
          gofmt.enable = true;
          nixfmt.enable = true;
        };
      };

      devShells.default = pkgs.mkShell {
        packages = with pkgs; [
          go
          gopls
          gcc
          pkg-config
          cliproxyapi
          curl
          git
          jq
        ];

        MAGPIE_PATCHED_SRC = "${pkgs.magpie-patched}";

        shellHook = ''
          if [ -x ./scripts/prepare.sh ]; then
            ./scripts/prepare.sh
          fi
        '';
      };
    };
}
