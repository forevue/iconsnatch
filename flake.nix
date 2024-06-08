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

              CGO_ENABLED = 0;

              ldflags = [ "-X faviconapi/defaults.CacheStatus=enabled" ];
                  vendorHash = "sha256-Bs1Ni2r8Fs3LVfYFRT85dwttVYZpCQBeQkbl4ta6Ug8=";
                  src = ./.;
              };

          devShells.default = pkgs.mkShell {
            packages = with pkgs; [ go ];
          };
        })
    );
}
