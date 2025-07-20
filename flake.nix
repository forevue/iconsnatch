{
  description = "A basic gomod2nix flake";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  inputs.flake-utils.url = "github:numtide/flake-utils";

  outputs = { self, nixpkgs, flake-utils }:
    (flake-utils.lib.eachDefaultSystem
      (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          packages.default = pkgs.buildGoModule {
              name = "app";
              version = "dev";

              env.CGO_ENABLED = 0;

              ldflags = [];
                  vendorHash = "sha256-ad+zwySR/xCC3OyurtjHSg8o8p6/g05PKzLcL8ZxSkU=";
                  src = ./.;
              };

          devShells.default = pkgs.mkShell {
            packages = with pkgs; [ go ];
          };
        })
    ) // {
      nixosModules.default = { config, lib, pkgs, ... }: {
        imports = [ (import ./nixos/module.nix) ];
        config = lib.mkIf config.services.iconsnatch.enable {
          services.iconsnatch.package =
            lib.mkDefault self.packages.${pkgs.system}.default;
        };
      };
    };
}
