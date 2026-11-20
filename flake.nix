{
  description = "Modpack Manager: edit and export Minecraft modpacks (Modrinth + CurseForge)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems =
        f: nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
    in
    {
      # nix build .#gtk | .#qt | .#default (both frontends plus the launcher)
      packages = forAllSystems (
        _system: pkgs: rec {
          modpackmanager = pkgs.callPackage ./nix/package.nix { };
          gtk = modpackmanager.override { withQt = false; };
          qt = modpackmanager.override { withGtk = false; };
          default = modpackmanager;
        }
      );

      # `nix flake check` builds, and so tests, both variants.
      checks = forAllSystems (
        system: _pkgs: {
          inherit (self.packages.${system}) gtk qt;
        }
      );

      devShells = forAllSystems (
        system: pkgs: {
          default = pkgs.mkShell {
            inputsFrom = [ self.packages.${system}.default ];
            packages = with pkgs; [
              gopls
              delve
              shellcheck
              actionlint
            ];
            # Lets `go run ./cmd/modpackmanager-gtk` find GSettings schemas.
            shellHook = ''
              export XDG_DATA_DIRS="''${GSETTINGS_SCHEMAS_PATH:-}:''${XDG_DATA_DIRS:-}"
            '';
          };
        }
      );

      # pkgs.modpackmanager for NixOS / home-manager configurations:
      #   nixpkgs.overlays = [ inputs.modpackmanager.overlays.default ];
      overlays.default = final: _prev: {
        modpackmanager = final.callPackage ./nix/package.nix { };
      };

      formatter = forAllSystems (_system: pkgs: pkgs.nixfmt-tree);
    };
}