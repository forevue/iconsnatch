{ config, lib, pkgs, ... }:

with lib;

let
  cfg = config.services.iconsnatch;
in
{
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

    otelEndpoint = mkOption {
      type = types.nullOr types.str;
      default = null;
      description = lib.mdDoc "The OTLP exporter endpoint. For example, 'localhost:4317'.";
    };

    otelInsecure = mkOption {
      type = types.bool;
      default = false;
      description = lib.mdDoc "Use an insecure connection to the OTLP exporter.";
    };
  };

  config = mkIf cfg.enable {
    users.users.iconsnatch = {
      isSystemUser = true;
      group = "iconsnatch";
      description = "iconsnatch service user";
    };

    users.groups.iconsnatch = {
      isSystemGroup = true;
    };

    systemd.services.iconsnatch = {
      description = "iconsnatch - An icon fetching and caching service";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      serviceConfig = {
        Type = "simple";
        User = "iconsnatch";
        Group = "iconsnatch";
        StateDirectory = "iconsnatch";
        ExecStart =
          let
            flags = [
              "--listen-addr=${cfg.listenAddr}"
              "--public-url=${cfg.publicURL}"
              "--storage-dir=${cfg.storageDir}"
            ] ++ lib.optionals (cfg.otelEndpoint != null) [
              "--otel-exporter-otlp-endpoint=${cfg.otelEndpoint}"
            ] ++ lib.optionals (cfg.otelEndpoint != null && cfg.otelInsecure) [
              "--otel-exporter-otlp-insecure"
            ];
          in
          "${cfg.package}/bin/app ${lib.concatStringsSep " " flags}";
        Restart = "on-failure";
        RestartSec = "5s";

        # Hardening
        CapabilityBoundingSet = "";
        DeviceAllow = "";
        LockPersonality = true;
        NoNewPrivileges = true;
        PrivateDevices = true;
        PrivateTmp = true;
        PrivateUsers = true;
        ProcSubset = "pid";
        ProtectClock = true;
        ProtectControlGroups = true;
        ProtectHome = true;
        ProtectHostname = true;
        ProtectKernelLogs = true;
        ProtectKernelModules = true;
        ProtectKernelTunables = true;
        ProtectProc = "invisible";
        ProtectSystem = "strict";
        RemoveIPC = true;
        RestrictAddressFamilies = [ "AF_INET" "AF_INET6" ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        SystemCallArchitectures = "native";
        SystemCallFilter = "@system-service";
        UMask = "0077";
      };
    };
  };
}
