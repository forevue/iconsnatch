{ config, lib, pkgs, ... }:

with lib;

let cfg = config.services.iconsnatch;
in {
  options.services.iconsnatch = {
    enable = mkEnableOption (lib.mdDoc "iconsnatch server");

    package = mkOption {
      type = types.package;
      description = lib.mdDoc "The iconsnatch package to use.";
    };

    listenAddr = mkOption {
      type = types.str;
      default = ":8080";
      description = lib.mdDoc "The address to listen on for HTTP requests.";
    };

    publicURL = mkOption {
      type = types.str;
      default = "http://localhost:8080";
      description = lib.mdDoc "The public base URL for constructing icon URLs.";
    };

    storageDir = mkOption {
      type = types.path;
      default = "/var/lib/iconsnatch/icons";
      description = lib.mdDoc "The directory to store icons in.";
    };

    environment = mkOption {
      type = types.attrsOf types.str;
      default = { };
      description = lib.mdDoc "Environment variables to pass to the iconsnatch service.";
    };
  };

  config = mkIf cfg.enable {
    users.users.iconsnatch = {
      isSystemUser = true;
      group = "iconsnatch";
      description = "iconsnatch service user";
    };

    users.groups.iconsnatch = { };

    systemd.services.iconsnatch = {
      description = "iconsnatch - An icon fetching and caching service";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      environment = cfg.environment;

      serviceConfig = {
        Type = "simple";
        User = "iconsnatch";
        Group = "iconsnatch";
        StateDirectory = "iconsnatch";
        ExecStart = let
          flags = [
            "--listen-addr=${cfg.listenAddr}"
            "--public-url=${cfg.publicURL}"
            "--storage-dir=${cfg.storageDir}"
          ];
        in "${cfg.package}/bin/iconsnatch ${lib.concatStringsSep " " flags}";
        Restart = "on-failure";
        RestartSec = "5s";
      };
    };
  };
}
