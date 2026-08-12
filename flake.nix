{
  description = "DearMachine Client";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system);
      packageSet = system:
        let
          pkgs = import nixpkgs { inherit system; };
          lib = pkgs.lib;
          patchedGo = pkgs.go.overrideAttrs (_: {
            version = "1.26.5";
            src = pkgs.fetchurl {
              url = "https://go.dev/dl/go1.26.5.src.tar.gz";
              hash = "sha256-SVvkvIcXasVnOS5bQRar2YRm0z17SdQedkzMaXay3EI=";
            };
          });
          revision = self.rev or "unknown";
          shortRevision =
            if revision == "unknown"
            then revision
            else builtins.substring 0 12 revision;
          source = lib.cleanSourceWith {
            src = ./dearmachine;
            filter = path: type:
              let
                rel = lib.removePrefix (toString ./dearmachine + "/") (toString path);
              in
                rel != "agent-manager"
                && rel != "device-client"
                && rel != "result"
                && !(lib.hasSuffix ".bak" rel);
          };
          dearmachine = (pkgs.buildGoModule.override { go = patchedGo; }) {
            pname = "dearmachine";
            version = "0.1.0-${shortRevision}";
            src = source;
            subPackages = [ "cmd/dearmachine" ];
            vendorHash = "sha256-sU+aA2uXwKyUPM/BydYy7vJUDHzKXH3D8ANT1p8KwIA=";
            env.CGO_ENABLED = 1;
            doCheck = false;
            preBuild = ''
              export GOFLAGS="$GOFLAGS -buildvcs=false"
            '';
            nativeBuildInputs = [ pkgs.makeWrapper ];
            nativeCheckInputs = [ pkgs.bash pkgs.coreutils pkgs.gitMinimal ];
            checkPhase = ''
              runHook preCheck
              go test ./...
              runHook postCheck
            '';
            postInstall = ''
              wrapProgram $out/bin/dearmachine \
                --prefix PATH : ${lib.makeBinPath [ pkgs.gitMinimal ]}
            '';
            meta.mainProgram = "dearmachine";
          };
          goTests = dearmachine.overrideAttrs (_: {
            pname = "dearmachine-go-tests";
            doCheck = true;
          });
          install = pkgs.writeShellApplication {
            name = "dearmachine-install";
            runtimeInputs = [ pkgs.coreutils ];
            checkPhase = ''
              runHook preCheck
              ${pkgs.stdenv.shellDryRun} "$target"
              runHook postCheck
            '';
            text = ''
              : "''${HOME:?HOME must be set to the target home directory}"

              install -d -m 0755 "$HOME/.local/bin"
              install -d -m 0700 \
                "$HOME/.dearmachine" \
                "$HOME/.dearmachine/config" \
                "$HOME/.dearmachine/state" \
                "$HOME/.dearmachine/run" \
                "$HOME/.dearmachine/log"
              install -m 0755 \
                ${dearmachine}/bin/dearmachine \
                "$HOME/.local/bin/dearmachine"

              printf 'Installed dearmachine at %s\n' "$HOME/.local/bin/dearmachine"
            '';
          };
        in {
          inherit pkgs dearmachine goTests install;
        };
    in {
      packages = forAllSystems (system:
        let project = packageSet system;
        in {
          inherit (project) dearmachine install;
          default = project.dearmachine;
        });

      apps = forAllSystems (system: {
        default = {
          type = "app";
          program = "${self.packages.${system}.dearmachine}/bin/dearmachine";
        };
        dearmachine = self.apps.${system}.default;
        install = {
          type = "app";
          program = "${self.packages.${system}.install}/bin/dearmachine-install";
        };
      });

      checks = forAllSystems (system:
        let
          project = packageSet system;
          pkgs = project.pkgs;
        in {
          inherit (project) dearmachine;
          go-tests = project.goTests;
          smoke = pkgs.runCommand "dearmachine-smoke" {
            nativeBuildInputs = [ pkgs.gnugrep ];
          } ''
            ${project.dearmachine}/bin/dearmachine --help > $out 2>&1
            grep -F "Usage of dearmachine" $out
          '';
          install-smoke = pkgs.runCommand "dearmachine-install-smoke" {
            nativeBuildInputs = [ project.install ];
          } ''
            export HOME="$TMPDIR/home"
            dearmachine-install
            test -x "$HOME/.local/bin/dearmachine"
            for directory in config state run log; do
              test -d "$HOME/.dearmachine/$directory"
            done
            "$HOME/.local/bin/dearmachine" --help 2>&1 | grep -F "Usage of dearmachine"
            touch $out
          '';
        });
    };
}
