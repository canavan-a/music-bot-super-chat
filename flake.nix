{
  description = "Super Chat music bot";

  inputs.nixpkgs.url = "github:nixos/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: { default = pkgs.callPackage ./package.nix { }; });

      # nix run . -- init / run / join <link>
      apps = forAll (pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${pkgs.stdenv.hostPlatform.system}.default}/bin/music-bot";
        };
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell { packages = with pkgs; [ go gopls ffmpeg-headless yt-dlp ]; };
      });

      nixosModules.default = import ./module.nix self;
    };
}
