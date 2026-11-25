# NixOS module: services.music-bot.
#
#   services.music-bot = {
#     enable = true;
#     passwordFile = "/run/secrets/music-bot-password"; # the admin password, for login in chat
#   };
#
# The first start makes the bot's account; its recovery phrase is written to
# /var/lib/music-bot/init-output.txt (readable only by the service). Then, on
# the box: `music-bot join <invite link>`.
self:
{ config, lib, pkgs, ... }:
let
  cfg = config.services.music-bot;
  dataDir = "/var/lib/music-bot/data";
  # `music-bot join <link>` etc., as the service's user, on its data.
  wrapper = pkgs.writeShellScriptBin "music-bot" ''
    exec ${pkgs.sudo}/bin/sudo -u music-bot SUPERCHAT_BOT_DATA=${dataDir} ${lib.getExe cfg.package} "$@"
  '';
in
{
  options.services.music-bot = {
    enable = lib.mkEnableOption "the Super Chat music bot";
    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      description = "The music bot package.";
    };
    passwordFile = lib.mkOption {
      type = lib.types.path;
      description = "File holding the admin password (used once, to make the account).";
    };
    name = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "The bot's display name when its account is made (default: a random first name). Change it later in chat.";
    };
    idle = lib.mkOption {
      type = lib.types.str;
      default = "5m";
      description = "Leave voice after this long with nobody else in it, or nothing playing or queued.";
    };
  };

  config = lib.mkIf cfg.enable {
    users.users.music-bot = { isSystemUser = true; group = "music-bot"; home = "/var/lib/music-bot"; };
    users.groups.music-bot = { };
    environment.systemPackages = [ wrapper ];

    systemd.services.music-bot = {
      description = "Super Chat music bot";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      environment = {
        SUPERCHAT_BOT_DATA = dataDir;
        MUSIC_BOT_IDLE = cfg.idle;
      };
      script = ''
        if [ ! -e ${dataDir}/account.key ]; then
          umask 077
          SUPERCHAT_BOT_PASSWORD="$(cat "$CREDENTIALS_DIRECTORY/password")" \
            ${lib.getExe cfg.package} init ${lib.optionalString (cfg.name != "") "--name ${lib.escapeShellArg cfg.name}"} \
            > /var/lib/music-bot/init-output.txt
        fi
        exec ${lib.getExe cfg.package} run
      '';
      serviceConfig = {
        User = "music-bot";
        Group = "music-bot";
        StateDirectory = "music-bot";
        StateDirectoryMode = "0700";
        LoadCredential = "password:${cfg.passwordFile}";
        Restart = "always";
        RestartSec = 5;
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
      };
    };
  };
}
