{
  description = "DearMachine Client";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
  inputs.nixpkgs-codex.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs, nixpkgs-codex }:
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
          # Unstable no longer evaluates on Intel macOS.
          codexPkgs = import (if system == "x86_64-darwin" then nixpkgs else nixpkgs-codex) { inherit system; };
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
            subPackages = [ "cmd/dearmachine" "cmd/agent-manager" ];
            vendorHash = "sha256-jZaVeCI9Vnu1MjLLslu6OlFOlSrzWgekjt2Z9KhIa4o=";
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
                --prefix PATH : $out/bin:${lib.makeBinPath [ pkgs.gitMinimal ]}
            '';
            meta.mainProgram = "dearmachine";
          };
          agentManager = dearmachine.overrideAttrs (_: {
            pname = "dearmachine-agent-manager";
            subPackages = [ "cmd/agent-manager" ];
            postInstall = "";
            meta.mainProgram = "agent-manager";
          });
          nativeServiceLauncher = if pkgs.stdenv.isLinux then pkgs.writeShellApplication {
            name = "dearmachine-native-service";
            runtimeInputs = [ pkgs.systemd ];
            text = builtins.readFile ./scripts/nix/native-service-launch.sh;
          } else null;
          nativeServiceLifecycle = if pkgs.stdenv.isLinux then pkgs.writeShellApplication {
            name = "dearmachine-native-service-lifecycle";
            runtimeInputs = with pkgs; [ coreutils systemd ];
            text = builtins.readFile ./scripts/nix/native-service-lifecycle.sh;
          } else null;
          codexTool = codexPkgs.codex;
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
              install -m 0755 \
                ${dearmachine}/bin/agent-manager \
                "$HOME/.local/bin/agent-manager"

              printf 'Installed dearmachine and agent-manager in %s\n' "$HOME/.local/bin"
            '';
          };
          containerEntrypoint = pkgs.writeShellScriptBin "dearmachine-container-entrypoint" ''
            set -eu

            backend_environment_file="''${DEARMACHINE_BACKEND_ENVIRONMENT_FILE:-}"
            if [ -n "$backend_environment_file" ]; then
              if [ ! -r "$backend_environment_file" ]; then
                printf 'DEARMACHINE_BACKEND_ENVIRONMENT_FILE is not readable: %s\n' \
                  "$backend_environment_file" >&2
                exit 1
              fi
              while IFS= read -r environment_line || [ -n "$environment_line" ]; do
                case "$environment_line" in
                  ""|'#'*) continue ;;
                  *=*)
                    environment_name=''${environment_line%%=*}
                    case "$environment_name" in
                      ""|[0-9]*|*[!A-Za-z0-9_]*)
                        printf 'invalid backend environment variable name: %s\n' \
                          "$environment_name" >&2
                        exit 1
                        ;;
                    esac
                    export "$environment_line"
                    ;;
                  *)
                    printf 'invalid backend environment file line\n' >&2
                    exit 1
                    ;;
                esac
              done < "$backend_environment_file"
            fi

            if [ -n "''${AGENTMAIL_API_KEY_FILE:-}" ]; then
              if [ ! -r "$AGENTMAIL_API_KEY_FILE" ]; then
                printf 'AGENTMAIL_API_KEY_FILE is not readable: %s\n' \
                  "$AGENTMAIL_API_KEY_FILE" >&2
                exit 1
              fi
              IFS= read -r AGENTMAIL_API_KEY < "$AGENTMAIL_API_KEY_FILE" || \
                [ -n "$AGENTMAIL_API_KEY" ]
              export AGENTMAIL_API_KEY
            fi

            exec ${lib.getExe dearmachine} "$@"
          '';
          containerHealth = pkgs.writeShellScriptBin "dearmachine-health" ''
            set -eu

            pidfile="''${DEARMACHINE_PIDFILE:-/home/dearmachine/.dearmachine/run/dearmachine.pid}"
            readyfile="$(dirname "$pidfile")/dearmachine.ready"
            [ -r "$pidfile" ]
            IFS= read -r pid < "$pidfile"
            case "$pid" in
              *[!0-9]*|"") exit 1 ;;
            esac
            [ -r "$readyfile" ]
            IFS= read -r ready_pid < "$readyfile"
            [ "$ready_pid" = "$pid" ]
            kill -0 "$pid"
            grep -aq dearmachine "/proc/$pid/cmdline"
          '';
          imageBase = {
            created = "1970-01-01T00:00:01Z";
            extraCommands = ''
              mkdir -p tmp home/dearmachine opt/dearmachine/bin run/secrets usr/bin workspace
              ln -s ../../bin/env usr/bin/env
              chmod 1777 tmp
              chmod 0700 home/dearmachine run/secrets
            '';
          };
          dearmachineImage = pkgs.dockerTools.buildLayeredImage (imageBase // {
            name = "localhost/dearmachine";
            tag = "nix";
            contents = [
              dearmachine
              containerEntrypoint
              containerHealth
              pkgs.busybox
              pkgs.cacert
            ];
            config = {
              Entrypoint = [ (lib.getExe containerEntrypoint) ];
              Env = [
                "DEARMACHINE_HOME=/home/dearmachine/.dearmachine"
                "DEARMACHINE_PIDFILE=/home/dearmachine/.dearmachine/run/dearmachine.pid"
                "HOME=/home/dearmachine"
                "PATH=/opt/dearmachine/bin:/bin"
                "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
                "XDG_CACHE_HOME=/home/dearmachine/.cache"
                "XDG_CONFIG_HOME=/home/dearmachine/.config"
                "XDG_STATE_HOME=/home/dearmachine/.local/state"
              ];
              WorkingDir = "/workspace";
              Labels = {
                "org.opencontainers.image.title" = "DearMachine Client";
                "org.opencontainers.image.revision" = revision;
              };
            };
          });
          composeBundle = pkgs.runCommand "dearmachine-compose" { } ''
            mkdir -p $out/share/dearmachine/compose
            cp ${./deploy/compose}/*.yaml $out/share/dearmachine/compose/
          '';
          composeCheck = pkgs.runCommand "dearmachine-compose-check" {
            nativeBuildInputs = [ (pkgs.python3.withPackages (p: [ p.pyyaml ])) ];
          } ''
            python - <<'PY'
            import pathlib
            import yaml

            root = pathlib.Path("${composeBundle}/share/dearmachine/compose")
            base = yaml.safe_load((root / "compose.yaml").read_text())
            production = yaml.safe_load((root / "compose.production.yaml").read_text())
            test = yaml.safe_load((root / "compose.test.yaml").read_text())

            assert set(base["services"]) == {"dearmachine"}
            service = base["services"]["dearmachine"]
            assert service["image"] == "''${DEARMACHINE_IMAGE:-localhost/dearmachine:nix}"
            assert service["restart"] == "unless-stopped"
            assert service["working_dir"] == "''${DEARMACHINE_CONTAINER_PROJECT:-/workspace}"
            assert service["command"][service["command"].index("--project") + 1] == service["working_dir"]
            assert service["healthcheck"]["test"] == ["CMD", "dearmachine-health"]
            assert service["command"][0] == "up"
            assert service["command"][1] == "--foreground"
            assert "--magnifica-humanitas" in service["command"]
            assert service["command"].count("--magnifica-humanitas") == 1
            assert "--inbox-id" not in service["command"]
            assert "--db" not in service["command"]
            assert "--pidfile" not in service["command"]
            assert "--allow" not in service["command"]
            assert "--agent-manager" not in service["command"]
            assert service["environment"]["HOME"] == "/home/dearmachine"
            for directory in ("config", "state", "run", "log"):
                assert any(
                    f"/home/dearmachine/.dearmachine/{directory}" in volume
                    for volume in service["volumes"]
                )
            assert any(
                "/home/dearmachine/.dearmachine/agent-manager" in volume
                for volume in service["volumes"]
            )
            assert any("/home/dearmachine/.machtiani" in volume for volume in service["volumes"])
            assert any("/workspace" in volume for volume in service["volumes"])
            assert any("/opt/dearmachine/bin" in volume for volume in service["volumes"])
            assert any("/nix/store" in volume for volume in service["volumes"])
            assert set(production["secrets"]) == {"dearmachine_agentmail_api_key"}
            assert production["services"]["dearmachine"]["environment"] == {
                "AGENTMAIL_API_KEY": "",
                "AGENTMAIL_API_KEY_FILE": "/run/secrets/dearmachine_agentmail_api_key",
                "OPENMAIL_API_KEY": "",
                "OPENMAIL_API_KEY_FILE": "/run/secrets/dearmachine_agentmail_api_key",
                "SENDMUX_API_KEY": "",
                "SENDMUX_API_KEY_FILE": "/run/secrets/dearmachine_agentmail_api_key",
                "DEARMACHINE_BACKEND_ENVIRONMENT_FILE": "/home/dearmachine/.config/dearmachine/backends.env",
            }
            assert production["services"]["dearmachine"]["secrets"] == [
                "dearmachine_agentmail_api_key"
            ]
            assert test["services"]["dearmachine"]["entrypoint"] == [
                "/bin/sh", "/opt/dearmachine/bin/test-service.sh"
            ]
            assert "healthcheck" not in test["services"]["dearmachine"]
            PY
            touch $out
          '';
          stackRuntime = pkgs.writeShellApplication {
            name = "dearmachine-stack";
            runtimeInputs = with pkgs; [
              coreutils
              fuse-overlayfs
              gnugrep
              podman
              podman-compose
              python3
            ];
            text = ''
              export DEARMACHINE_COMPOSE_DIR="''${DEARMACHINE_COMPOSE_DIR:-${composeBundle}/share/dearmachine/compose}"
              export DEARMACHINE_FUSE_OVERLAYFS="''${DEARMACHINE_FUSE_OVERLAYFS:-${lib.getExe pkgs.fuse-overlayfs}}"
              export DEARMACHINE_IMAGE_ARCHIVE="''${DEARMACHINE_IMAGE_ARCHIVE:-${dearmachineImage}}"
              export DEARMACHINE_BOUNDARY_CHECK=${./scripts/nix/container-boundary.py}
              ${builtins.readFile ./scripts/nix/stack-runtime.sh}
            '';
          };
          hostLifecycle = pkgs.writeShellApplication {
            name = "dearmachine-host-lifecycle";
            runtimeInputs = with pkgs; [
              coreutils
              gnugrep
              nix
              psmisc
              systemd
              stackRuntime
            ];
            text = ''
              export DEARMACHINE_RUNTIME_PATH="''${DEARMACHINE_RUNTIME_PATH:-${stackRuntime}}"
              export DEARMACHINE_IMAGE_SOURCE="''${DEARMACHINE_IMAGE_SOURCE:-${dearmachineImage}}"
              export DEARMACHINE_UNIT_TEMPLATE="''${DEARMACHINE_UNIT_TEMPLATE:-${./contrib/systemd/dearmachine-stack.service.in}}"
              ${builtins.readFile ./scripts/nix/host-lifecycle.sh}
            '';
          };
          lifecycleApp = name: command: pkgs.writeShellApplication {
            inherit name;
            runtimeInputs = [ hostLifecycle ];
            text = ''
              exec dearmachine-host-lifecycle ${command} "$@"
            '';
          };
          hostInstall = lifecycleApp "dearmachine-host-install" "install";
          hostUpgrade = lifecycleApp "dearmachine-host-upgrade" "upgrade";
          hostUninstall = lifecycleApp "dearmachine-host-uninstall" "uninstall";
          hostSecrets = lifecycleApp "dearmachine-host-secrets" "secrets";
          containerLifecycle = pkgs.writeShellApplication {
            name = "dearmachine-container-lifecycle";
            runtimeInputs = [ hostLifecycle ];
            text = ''
              exec dearmachine-host-lifecycle "$@"
            '';
          };
          containerLifecycleApp = name: command: pkgs.writeShellApplication {
            inherit name;
            runtimeInputs = [ containerLifecycle ];
            text = ''
              exec dearmachine-container-lifecycle ${command} "$@"
            '';
          };
          containerInstall = containerLifecycleApp "dearmachine-container-install" "install";
          containerUpgrade = containerLifecycleApp "dearmachine-container-upgrade" "upgrade";
          containerUninstall = containerLifecycleApp "dearmachine-container-uninstall" "uninstall";
          containerSecrets = containerLifecycleApp "dearmachine-container-secrets" "secrets";
          unitCheck = pkgs.runCommand "dearmachine-systemd-user-unit-check" {
            nativeBuildInputs = [ pkgs.systemd ];
          } ''
            export HOME="$TMPDIR/home"
            export XDG_CONFIG_HOME="$TMPDIR/config"
            export XDG_DATA_HOME="$TMPDIR/data"
            export XDG_RUNTIME_DIR="$TMPDIR/run"
            export SYSTEMD_UNIT_PATH="$TMPDIR:${pkgs.systemd}/example/systemd/user"
            mkdir -m 0700 "$HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$XDG_RUNTIME_DIR"
            touch "$TMPDIR/runtime.env" "$TMPDIR/stack.env"
            substitute ${./contrib/systemd/dearmachine-stack.service.in} \
              "$TMPDIR/dearmachine-stack.service" \
              --replace-fail '@RELEASE@' 'hermetic-check' \
              --replace-fail '@RUNTIME_ENV@' "$TMPDIR/runtime.env" \
              --replace-fail '@STACK_ENV@' "$TMPDIR/stack.env" \
              --replace-fail '@STACK_EXEC@' '${stackRuntime}/bin/dearmachine-stack'
            systemd-analyze --user --man=no --generators=no \
              verify "$TMPDIR/dearmachine-stack.service"
            touch $out
          '';
          hostLifecycleCheck = pkgs.runCommand "dearmachine-host-lifecycle-check" {
            nativeBuildInputs = with pkgs; [ bash coreutils gnugrep ];
          } ''
            PROJECT_ROOT=${./.} bash ${./tests/nix/test-host-lifecycle.sh}
            touch $out
          '';
          stackRuntimeCheck = pkgs.runCommand "dearmachine-stack-runtime-check" {
            nativeBuildInputs = with pkgs; [ bash coreutils gnugrep python3 ];
          } ''
            PROJECT_ROOT=${./.} bash ${./tests/nix/test-stack-runtime.sh}
            PROJECT_ROOT=${./.} python3 ${./tests/nix/test-container-boundary.py}
            touch $out
          '';
          runbookCheck = pkgs.runCommand "dearmachine-runbook-contract-check" {
            nativeBuildInputs = [ pkgs.bash pkgs.gnugrep ];
          } ''
            PROJECT_ROOT=${./.} bash ${./tests/nix/test-runbook-contracts.sh}
            touch $out
          '';
          nativeServiceLauncherCheck = pkgs.runCommand "dearmachine-native-service-launcher-check" {
            nativeBuildInputs = with pkgs; [ bash coreutils gnugrep ];
          } ''
            PROJECT_ROOT=${./.} \
              NATIVE_SERVICE_LAUNCHER=${nativeServiceLauncher}/bin/dearmachine-native-service \
              bash ${./tests/nix/test-native-service-launch.sh}
            touch $out
          '';
          nativeServiceLifecycleCheck = pkgs.runCommand "dearmachine-native-service-lifecycle-check" {
            nativeBuildInputs = with pkgs; [ bash coreutils gnugrep systemd ];
          } ''
            PROJECT_ROOT=${./.} \
              NATIVE_SERVICE_LIFECYCLE=${nativeServiceLifecycle}/bin/dearmachine-native-service-lifecycle \
              SYSTEMD_EXAMPLE_USER=${pkgs.systemd}/example/systemd/user \
              bash ${./tests/nix/test-native-service-lifecycle.sh}
            touch $out
          '';
          shellCheck = pkgs.runCommand "dearmachine-shellcheck" {
            nativeBuildInputs = [ pkgs.shellcheck ];
          } ''
            shellcheck \
              ${./scripts/nix/host-lifecycle.sh} \
              ${./scripts/nix/native-service-lifecycle.sh} \
              ${./scripts/nix/native-service-launch.sh} \
              ${./scripts/nix/stack-runtime.sh} \
              ${./tests/nix/test-host-lifecycle.sh} \
              ${./tests/nix/test-native-service-lifecycle.sh} \
              ${./tests/nix/test-native-service-launch.sh} \
              ${./tests/nix/test-host-podman-integration.sh} \
              ${./tests/nix/test-stack-runtime.sh} \
              ${./tests/nix/test-runbook-contracts.sh}
            touch $out
          '';
        in {
          inherit
            pkgs dearmachine agentManager nativeServiceLauncher nativeServiceLifecycle codexTool goTests install dearmachineImage composeBundle
            composeCheck stackRuntime hostLifecycle
            hostInstall hostUpgrade hostUninstall hostSecrets
            containerLifecycle containerInstall containerUpgrade containerUninstall
            containerSecrets
            unitCheck hostLifecycleCheck stackRuntimeCheck runbookCheck nativeServiceLauncherCheck nativeServiceLifecycleCheck shellCheck;
        };
    in {
      packages = forAllSystems (system:
        let project = packageSet system;
        in {
          inherit (project) dearmachine install;
          agent-manager = project.agentManager;
          codex-tool = project.codexTool;
          default = project.dearmachine;
        } // project.pkgs.lib.optionalAttrs project.pkgs.stdenv.isLinux {
          dearmachine-image = project.dearmachineImage;
          dearmachine-compose = project.composeBundle;
          dearmachine-stack = project.stackRuntime;
          dearmachine-host-lifecycle = project.hostLifecycle;
          dearmachine-container-lifecycle = project.containerLifecycle;
          dearmachine-native-service = project.nativeServiceLauncher;
          dearmachine-native-service-lifecycle = project.nativeServiceLifecycle;
        });

      apps = forAllSystems (system:
        let project = packageSet system;
        in {
          default = {
            type = "app";
            program = "${self.packages.${system}.dearmachine}/bin/dearmachine";
          };
          dearmachine = self.apps.${system}.default;
          install = {
            type = "app";
            program = "${self.packages.${system}.install}/bin/dearmachine-install";
          };
        } // project.pkgs.lib.optionalAttrs project.pkgs.stdenv.isLinux {
          dearmachine-stack = {
            type = "app";
            program = "${self.packages.${system}.dearmachine-stack}/bin/dearmachine-stack";
          };
          dearmachine-host-lifecycle = {
            type = "app";
            program = "${self.packages.${system}.dearmachine-host-lifecycle}/bin/dearmachine-host-lifecycle";
          };
          dearmachine-native-service = {
            type = "app";
            program = "${project.nativeServiceLauncher}/bin/dearmachine-native-service";
          };
          dearmachine-native-service-lifecycle = {
            type = "app";
            program = "${project.nativeServiceLifecycle}/bin/dearmachine-native-service-lifecycle";
          };
          host-install = {
            type = "app";
            program = "${project.hostInstall}/bin/dearmachine-host-install";
          };
          host-upgrade = {
            type = "app";
            program = "${project.hostUpgrade}/bin/dearmachine-host-upgrade";
          };
          host-uninstall = {
            type = "app";
            program = "${project.hostUninstall}/bin/dearmachine-host-uninstall";
          };
          host-secrets = {
            type = "app";
            program = "${project.hostSecrets}/bin/dearmachine-host-secrets";
          };
          dearmachine-container-lifecycle = {
            type = "app";
            program = "${project.containerLifecycle}/bin/dearmachine-container-lifecycle";
          };
          container-install = {
            type = "app";
            program = "${project.containerInstall}/bin/dearmachine-container-install";
          };
          container-upgrade = {
            type = "app";
            program = "${project.containerUpgrade}/bin/dearmachine-container-upgrade";
          };
          container-uninstall = {
            type = "app";
            program = "${project.containerUninstall}/bin/dearmachine-container-uninstall";
          };
          container-secrets = {
            type = "app";
            program = "${project.containerSecrets}/bin/dearmachine-container-secrets";
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
            grep -F "Usage: dearmachine <command>" $out
            legacy=$(${project.dearmachine}/bin/dearmachine \
              --inbox-id smoke --db "$TMPDIR/smoke.db" --once 2>&1 || true)
            grep -F 'unknown command "--inbox-id"' <<<"$legacy" > /dev/null
          '';
          install-smoke = pkgs.runCommand "dearmachine-install-smoke" {
            nativeBuildInputs = [ project.install ];
          } ''
            export HOME="$TMPDIR/home"
            dearmachine-install
            test -x "$HOME/.local/bin/dearmachine"
            test -x "$HOME/.local/bin/agent-manager"
            for directory in config state run log; do
              test -d "$HOME/.dearmachine/$directory"
            done
            "$HOME/.local/bin/dearmachine" --help 2>&1 | grep -F "Usage: dearmachine <command>"
            "$HOME/.local/bin/agent-manager" --help 2>&1 | grep -F "agent-manager <command>"
            touch $out
          '';
        } // pkgs.lib.optionalAttrs pkgs.stdenv.isLinux {
          image = project.dearmachineImage;
          compose = project.composeCheck;
          host-lifecycle = project.hostLifecycle;
          host-lifecycle-test = project.hostLifecycleCheck;
          stack-runtime-test = project.stackRuntimeCheck;
          systemd-user-unit = project.unitCheck;
          runbook-contracts = project.runbookCheck;
          native-service-launcher = project.nativeServiceLauncherCheck;
          native-service-lifecycle = project.nativeServiceLifecycleCheck;
          shellcheck = project.shellCheck;
        });
    };
}
