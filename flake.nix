{
  description = "CLIProxyAPI reversible redaction plugin powered by Magpie";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    systems.url = "github:nix-systems/default";
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
      inputs.nixpkgs-lib.follows = "nixpkgs";
    };
    magpie = {
      url = "github:yetone/magpie/v0.1.4650";
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
          };

          overlayAttrs = {
            inherit (config.packages) cpa-reductant;
          };
        };
    };
}
