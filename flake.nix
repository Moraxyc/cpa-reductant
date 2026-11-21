{
  description = "CLIProxyAPI reversible redaction plugin powered by Magpie";

  nixConfig = {
    extra-substituters = [ "https://nix-cache.qaq.li/cpa-reductant" ];
    extra-trusted-public-keys = [
      "cpa-reductant:MMH53Hi2EWPzqIR73luxnwvuK/+Mixft1TToFSSILZo="
    ];
  };

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    systems.url = "github:nix-systems/default";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
    magpie = {
      url = "github:yetone/magpie/v0.1.4657";
      flake = false;
    };
  };

  outputs =
    inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [
        inputs.flake-parts.flakeModules.easyOverlay
        inputs.flake-parts.flakeModules.partitions
      ];

      systems = import inputs.systems;

      partitionedAttrs = {
        checks = "dev";
        devShells = "dev";
        formatter = "dev";
      };

      partitions.dev = {
        extraInputsFlake = ./dev;
        module = ./dev/flake-module.nix;
      };

      perSystem =
        {
          config,
          pkgs,
          system,
          ...
        }:
        {
          _module.args.pkgs = import inputs.nixpkgs {
            inherit system;
            overlays = [
              (_final: prev: {
                magpie-patched = prev.applyPatches {
                  name = "magpie-patched";
                  src = inputs.magpie;
                  patches = [
                    ./patches/magpie/0001-expose-redact.patch
                    ./patches/magpie/0002-expose-guard.patch
                  ];
                };
              })
            ];
          };

          packages = {
            cpa-reductant = pkgs.callPackage ./nix/package.nix { };
            default = config.packages.cpa-reductant;
          }
          // pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
            portable-linux-amd64 = pkgs.callPackage ./nix/portable.nix {
              inherit (config.packages) cpa-reductant;
              goos = "linux";
              goarch = "amd64";
              zigTarget = "x86_64-linux-gnu.2.28";
              ext = ".so";
            };
            portable-linux-arm64 = pkgs.callPackage ./nix/portable.nix {
              inherit (config.packages) cpa-reductant;
              goos = "linux";
              goarch = "arm64";
              zigTarget = "aarch64-linux-gnu.2.28";
              ext = ".so";
            };
            portable-windows-amd64 = pkgs.callPackage ./nix/portable.nix {
              inherit (config.packages) cpa-reductant;
              goos = "windows";
              goarch = "amd64";
              zigTarget = "x86_64-windows-gnu";
              ext = ".dll";
            };
            portable-windows-arm64 = pkgs.callPackage ./nix/portable.nix {
              inherit (config.packages) cpa-reductant;
              goos = "windows";
              goarch = "arm64";
              zigTarget = "aarch64-windows-gnu";
              ext = ".dll";
            };
          };

          overlayAttrs = {
            inherit (config.packages) cpa-reductant;
          };
        };
    };
}
