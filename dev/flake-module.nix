{ inputs, ... }:
{
  imports = [
    inputs.treefmt-nix.flakeModule
  ];

  perSystem =
    { config, pkgs, ... }:
    {
      treefmt = {
        projectRootFile = "flake.nix";
        programs = {
          gofmt.enable = true;
          nixfmt.enable = true;
        };
      };

      checks = {
        dev-shell = config.devShells.default;
      }
      //
        pkgs.lib.optionalAttrs
          (pkgs.lib.meta.availableOn pkgs.stdenv.hostPlatform config.packages.cpa-reductant)
          {
            package = config.packages.cpa-reductant;
          }
      // pkgs.lib.optionalAttrs (pkgs.lib.meta.availableOn pkgs.stdenv.hostPlatform pkgs.cliproxyapi) {
        smoke = pkgs.callPackage ../nix/smoke-test.nix {
          inherit (config.packages) cpa-reductant;
        };
      }
      // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
        inherit (config.packages)
          portable-linux-amd64
          portable-linux-arm64
          portable-windows-amd64
          portable-windows-arm64
          ;
      }
      // pkgs.lib.optionalAttrs (pkgs.stdenv.hostPlatform.system == "x86_64-linux") {
        smoke-portable = pkgs.callPackage ../nix/smoke-test.nix {
          inherit (config.packages) cpa-reductant;
          pluginPkg = config.packages.portable-linux-amd64;
          name = "${config.packages.cpa-reductant.pname}-portable-smoke-test-${config.packages.cpa-reductant.version}";
        };
      }
      // pkgs.lib.optionalAttrs (pkgs.stdenv.hostPlatform.system == "aarch64-linux") {
        smoke-portable = pkgs.callPackage ../nix/smoke-test.nix {
          inherit (config.packages) cpa-reductant;
          pluginPkg = config.packages.portable-linux-arm64;
          name = "${config.packages.cpa-reductant.pname}-portable-smoke-test-${config.packages.cpa-reductant.version}";
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
